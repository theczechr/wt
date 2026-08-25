package herdr

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const snapshotWithAgents = `{"id":"cli:api","result":{"type":"session_snapshot","snapshot":{"panes":[
 {"pane_id":"w1:p1","cwd":"/r/server-dqs","agent":"claude","agent_status":"blocked",
  "foreground_cwd":"/somewhere/else","agent_session":{"kind":"id","value":"sess-1","agent":"claude"}},
 {"pane_id":"w1:p2","cwd":"/r/server-dqs/src","agent":"claude","agent_status":"working",
  "agent_session":{"kind":"id","value":"sess-2","agent":"claude"}},
 {"pane_id":"w2:p1","cwd":"/r/server-dqsfix","agent":"claude","agent_status":"idle"},
 {"pane_id":"w3:p1","cwd":"/r/plain-shell","agent":"","agent_status":"unknown"}
]}}}`

func liveIndex(t *testing.T) Index {
	t.Helper()
	t.Setenv("HERDR_SOCKET_PATH", writeFakeSocket(t))
	// One object per line: decode scans NDJSON, so the readable
	// multi-line fixture above has to be collapsed the way herdr emits it.
	fakeHerdr(t, strings.ReplaceAll(snapshotWithAgents, "\n", ""), 0)
	idx, err := Agents(context.Background())
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	return idx
}

// TestForMatchesSubdirectoriesButNotSiblingPrefixes is the join, and the
// place a naive strings.HasPrefix goes wrong. A pane launched deeper in the
// tree still belongs to its worktree, but "/r/server-dqs" must never claim
// "/r/server-dqsfix" -- the exact sibling-name shape this repo is full of.
func TestForMatchesSubdirectoriesButNotSiblingPrefixes(t *testing.T) {
	idx := liveIndex(t)

	by := idx.Attribute([]string{"/r/server-dqs", "/r/server-dqsfix", "/r/plain-shell"})
	got := by["/r/server-dqs"]
	if len(got) != 2 {
		t.Fatalf("got %d agents for /r/server-dqs, want 2 (the worktree and its src subdir)", len(got))
	}
	if s := Worst(got); s != StatusBlocked {
		t.Errorf("Worst = %q, want blocked: a blocked agent must outrank a working one", s)
	}

	sibling := by["/r/server-dqsfix"]
	if len(sibling) != 1 {
		t.Fatalf("sibling got %d agents, want 1 -- prefix matching leaked across worktrees", len(sibling))
	}
	if sibling[0].Status != StatusIdle {
		t.Errorf("sibling status = %q, want idle", sibling[0].Status)
	}
}

// TestPlainShellIsNotAnAgent guards the distinction herdr blurs: a pane with
// no agent reports agent_status "unknown", which must not be mistaken for an
// agent whose state could not be read.
func TestPlainShellIsNotAnAgent(t *testing.T) {
	by := liveIndex(t).Attribute([]string{"/r/plain-shell"})
	if got := by["/r/plain-shell"]; len(got) != 0 {
		t.Errorf("a plain shell was counted as %d agent(s)", len(got))
	}
}

// TestSessionIDIsCaptured pins the other join: herdr's agent_session value
// is the Claude session UUID, which is the same id wt reads out of
// transcripts.
func TestSessionIDIsCaptured(t *testing.T) {
	got := liveIndex(t).Attribute([]string{"/r/server-dqs"})["/r/server-dqs"]
	found := false
	for _, a := range got {
		if a.Session == "sess-1" {
			found = true
		}
	}
	if !found {
		t.Error("agent_session.value was not captured; the session join is dead")
	}
}

// TestAgentsDistinguishesAbsentFromUnreadable is the invariant this whole
// feature has to respect. Absent is benign and reports no agents; unreadable
// is unknown and must not. Collapsing them either blocks deletion forever
// for everyone without herdr, or authorises it while a daemon is silently
// failing.
func TestAgentsDistinguishesAbsentFromUnreadable(t *testing.T) {
	t.Run("no socket is absent", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "/definitely/not/a/socket")
		_, err := Agents(context.Background())
		if !errors.Is(err, ErrNotRunning) {
			t.Errorf("err = %v, want ErrNotRunning", err)
		}
	})

	t.Run("daemon up but failing is unreadable", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", writeFakeSocket(t))
		fakeHerdr(t, `{"id":"x","error":{"code":"busy","message":"try again"}}`, 1)
		_, err := Agents(context.Background())
		if err == nil {
			t.Fatal("expected an error")
		}
		if errors.Is(err, ErrNotRunning) {
			t.Error("a failing daemon was reported as absent; deletion would be authorised on unknown state")
		}
	})
}

func TestWorstRanksBlockedHighest(t *testing.T) {
	agents := []Agent{{Status: StatusIdle}, {Status: StatusDone}, {Status: StatusWorking}, {Status: StatusBlocked}}
	if got := Worst(agents); got != StatusBlocked {
		t.Errorf("Worst = %q, want blocked", got)
	}
	if got := Worst(nil); got != "" {
		t.Errorf("Worst(nil) = %q, want empty", got)
	}
}

// TestAttributeGivesNestedWorktreesToTheMostSpecificPath is the bug live
// data exposed and fixtures did not. A repo's nested worktrees sit INSIDE
// its primary checkout, so plain containment credits one agent to both rows.
// Observed on a real machine: a Claude pane in
// .../server/.claude/worktrees/crm-v1-changes-api was reported against that
// worktree and against .../server at once.
func TestAttributeGivesNestedWorktreesToTheMostSpecificPath(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", writeFakeSocket(t))
	fakeHerdr(t, `{"id":"x","result":{"snapshot":{"panes":[`+
		`{"pane_id":"p1","cwd":"/r/server/.claude/worktrees/feat","agent":"claude","agent_status":"working"}`+
		`]}}}`, 0)
	idx, err := Agents(context.Background())
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}

	by := idx.Attribute([]string{"/r/server", "/r/server/.claude/worktrees/feat"})
	if n := len(by["/r/server"]); n != 0 {
		t.Errorf("primary checkout claimed %d agent(s) belonging to a nested worktree", n)
	}
	if n := len(by["/r/server/.claude/worktrees/feat"]); n != 1 {
		t.Errorf("nested worktree got %d agents, want 1", n)
	}
}

func TestContainsRespectsPathSegments(t *testing.T) {
	if Contains("/r/server-dqs", "/r/server-dqsfix") {
		t.Error("sibling worktree matched on a bare string prefix")
	}
	if !Contains("/r/server-dqs", "/r/server-dqs/src/deep") {
		t.Error("a subdirectory did not match its worktree")
	}
	if !Contains("/r/server-dqs", "/r/server-dqs") {
		t.Error("exact path did not match itself")
	}
}

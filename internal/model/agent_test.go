package model

import "testing"

// TestPruneBlockersTreatsAbsentHerdrAsBenign is the case that would break
// every user who does not run herdr. Absent is a real answer -- with no
// daemon there genuinely are no agents -- so it must not block, even though
// it is the zero value of AgentProbe.
func TestPruneBlockersTreatsAbsentHerdrAsBenign(t *testing.T) {
	w := Workspace{Kind: KindSibling, StatusKnown: true, SessionsKnown: true} // AgentProbeAbsent by construction
	for _, b := range w.PruneBlockers() {
		t.Errorf("clean worktree blocked with no herdr running: %q", b)
	}
}

// TestPruneBlockersBlocksOnUnreadableHerdr is the other half. A daemon that
// is up but will not answer is unknown state, and unknown must never be
// reported as safe.
func TestPruneBlockersBlocksOnUnreadableHerdr(t *testing.T) {
	w := Workspace{Kind: KindSibling, StatusKnown: true, SessionsKnown: true, AgentProbe: AgentProbeUnreadable}
	if len(w.PruneBlockers()) == 0 {
		t.Error("unreadable herdr authorised deletion; unknown was treated as safe")
	}
}

// TestPruneBlockersBlocksBusyAgentsOnly checks which states count as "in
// use". working and blocked do. idle and done deliberately do not: an agent
// sitting at a prompt is exactly the long-lived session the user keeps on
// purpose, and blocking those forever would defeat the gate.
func TestPruneBlockersBlocksBusyAgentsOnly(t *testing.T) {
	cases := map[string]bool{"working": true, "blocked": true, "idle": false, "done": false, "": false}
	for status, wantBlocked := range cases {
		w := Workspace{Kind: KindSibling, StatusKnown: true, SessionsKnown: true, AgentProbe: AgentProbeOK, AgentStatus: status, AgentCount: 1}
		blocked := len(w.PruneBlockers()) > 0
		if blocked != wantBlocked {
			t.Errorf("status %q: blocked = %v, want %v", status, blocked, wantBlocked)
		}
	}
}

// TestHasBusyAgentRequiresASuccessfulProbe guards against a stale or
// hand-built Workspace claiming a busy agent it never actually observed.
func TestHasBusyAgentRequiresASuccessfulProbe(t *testing.T) {
	w := Workspace{AgentStatus: "working"} // probe never ran
	if w.HasBusyAgent() {
		t.Error("reported a busy agent without a successful probe")
	}
}

// TestPruneBlockersBlocksUnknownSessions closes the last of the four
// fail-open collectors named in discover's package comment. An unreadable
// session directory used to be indistinguishable from a worktree nobody ever
// opened Claude in, and both authorised deletion.
func TestPruneBlockersBlocksUnknownSessions(t *testing.T) {
	w := Workspace{Kind: KindSibling, StatusKnown: true} // SessionsKnown false
	blockers := w.PruneBlockers()
	if len(blockers) == 0 {
		t.Fatal("unknown session state authorised deletion")
	}
	found := false
	for _, b := range blockers {
		if b == "Claude sessions could not be read" {
			found = true
		}
	}
	if !found {
		t.Errorf("blockers %v do not name the session probe", blockers)
	}
}

// TestPruneBlockersAllowsAWorktreeThatNeverHostedASession is the benign
// counterpart: zero sessions is a real answer when the probe succeeded, and
// must not block.
func TestPruneBlockersAllowsAWorktreeThatNeverHostedASession(t *testing.T) {
	w := Workspace{Kind: KindSibling, StatusKnown: true, SessionsKnown: true}
	if b := w.PruneBlockers(); len(b) != 0 {
		t.Errorf("blockers = %v, want none for a worktree with a successful empty probe", b)
	}
}

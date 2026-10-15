package ephemeral_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
	"github.com/theczechr/ephemera/internal/model"
)

// status.showUntrackedFiles=no hides untracked files from a plain
// `git status --porcelain`, and a plain `git worktree remove` then deletes
// them. Every safety status read must override it.
func TestReapRefusesUntrackedFileUnderShowUntrackedNo(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/hidden-untracked")
	r.Git(t, r.Primary, "config", "status.showUntrackedFiles", "no")
	scratch := filepath.Join(wt, "scratch.txt")
	if err := os.WriteFile(scratch, []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); !blockersContain(b, "uncommitted") {
		t.Fatalf("an untracked file must block even with showUntrackedFiles=no, got %v", b)
	}
	if _, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()}); err == nil {
		t.Fatal("ReapWith must refuse")
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Error("the untracked file must survive:", err)
	}
}

func TestReapRefusesUntrackedFileInSubmoduleUnderShowUntrackedNo(t *testing.T) {
	r := gittest.New(t)
	r.AddSubmodule(t)
	cfg, wt := newEphemeral(t, r, "feat/hidden-sub-untracked")
	r.Git(t, wt, "submodule", "update", "--init", "-q")
	r.Git(t, r.Primary, "config", "status.showUntrackedFiles", "no")
	r.Git(t, filepath.Join(wt, "sub"), "config", "status.showUntrackedFiles", "no")
	scratch := filepath.Join(wt, "sub", "scratch.txt")
	if err := os.WriteFile(scratch, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) == 0 {
		t.Fatal("an untracked file inside a submodule must block even with showUntrackedFiles=no")
	}
	if _, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()}); err == nil {
		t.Fatal("ReapWith must refuse")
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Error("the file must survive:", err)
	}
}

// The pre-removal recheck must use the same override: a file written after
// inspect, invisible to a plain status, must still stop a --force removal.
func TestReapRecheckSeesLateUntrackedFileUnderShowUntrackedNo(t *testing.T) {
	r := gittest.New(t)
	r.AddSubmodule(t)
	cfg, wt := newEphemeral(t, r, "feat/hidden-late")
	r.Git(t, wt, "submodule", "update", "--init", "-q")
	r.Git(t, r.Primary, "config", "status.showUntrackedFiles", "no")
	r.Git(t, filepath.Join(wt, "sub"), "config", "status.showUntrackedFiles", "no")
	scratch := filepath.Join(wt, "sub", "late.txt")
	restore := ephemeral.SetBeforeRemovalForTest(func(string) {
		if err := os.WriteFile(scratch, []byte("x"), 0o600); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	defer restore()
	_, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "changed before removal") {
		t.Fatalf("err = %v, want a 'changed before removal' refusal", err)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Error("the file must survive:", err)
	}
}

// commitInSubmodule makes a commit inside wt/sub that exists on no remote and
// bumps the superproject's pointer to it with someone else's commit -- so the
// worktree is "done" (no own commits) and the only copy of the submodule
// commit lives in the worktree's own modules dir. It returns that commit.
func commitInSubmodule(t *testing.T, r *gittest.Repo, wt string, detached bool) string {
	t.Helper()
	sub := filepath.Join(wt, "sub")
	if detached {
		r.Git(t, sub, "checkout", "-q", "--detach")
	} else {
		r.Git(t, sub, "checkout", "-q", "-b", "subwork")
	}
	r.WriteCommit(t, sub, "subwork.txt", "only copy\n")
	sha := strings.TrimSpace(r.Git(t, sub, "rev-parse", "HEAD"))
	r.Git(t, wt, "add", "sub")
	r.Git(t, wt, "-c", "user.email=other@test.invalid", "commit", "-qm", "bump sub")
	return sha
}

func TestReapRefusesUnpushedSubmoduleCommit(t *testing.T) {
	for _, detached := range []bool{false, true} {
		name := map[bool]string{false: "on-a-branch", true: "detached"}[detached]
		t.Run(name, func(t *testing.T) {
			r := gittest.New(t)
			r.AddSubmodule(t)
			cfg, wt := newEphemeral(t, r, "feat/sub-commit-"+name)
			r.Git(t, wt, "submodule", "update", "--init", "-q")
			sha := commitInSubmodule(t, r, wt, detached)

			b := ephemeral.Blockers(context.Background(), cfg, wt)
			if !blockersContain(b, "exist on no remote") {
				t.Fatalf("an unpushed submodule commit must block, got %v", b)
			}
			if _, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()}); err == nil {
				t.Fatal("ReapWith must refuse")
			}
			if _, err := r.TryGit(filepath.Join(wt, "sub"), "cat-file", "-e", sha+"^{commit}"); err != nil {
				t.Errorf("the submodule commit must still exist: %v", err)
			}
		})
	}
}

// A submodule commit made after inspect must stop the --force removal too.
func TestReapRecheckRefusesLateSubmoduleCommit(t *testing.T) {
	r := gittest.New(t)
	r.AddSubmodule(t)
	cfg, wt := newEphemeral(t, r, "feat/sub-commit-late")
	r.Git(t, wt, "submodule", "update", "--init", "-q")
	var sha string
	restore := ephemeral.SetBeforeRemovalForTest(func(string) {
		sub := filepath.Join(wt, "sub")
		r.Git(t, sub, "checkout", "-q", "-b", "subwork")
		r.Git(t, sub, "commit", "-q", "--allow-empty", "-m", "late")
		sha = strings.TrimSpace(r.Git(t, sub, "rev-parse", "HEAD"))
		r.Git(t, sub, "checkout", "-q", "--detach", "HEAD~1")
	})
	defer restore()
	_, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "exist on no remote") {
		t.Fatalf("err = %v, want an 'exist on no remote' refusal", err)
	}
	if _, err := r.TryGit(filepath.Join(wt, "sub"), "cat-file", "-e", sha+"^{commit}"); err != nil {
		t.Errorf("the submodule commit must still exist: %v", err)
	}
}

// `wt feat/x` spawns its background sweep before it touches the marker, so
// the sweep can inspect the old last_opened_at. The grace must be re-applied
// against a fresh read of the marker immediately before removal.
func TestReapRefusesWhenReopenedBeforeRemoval(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/reopened")
	old := time.Now().Add(-time.Hour)
	if err := ephemeral.UpdateMarker(wt, func(m *ephemeral.Marker) { m.CreatedAt = old }); err != nil {
		t.Fatal(err)
	}
	opts := ephemeral.InspectOptions{Grace: ephemeral.DefaultSweepGrace}
	if d := ephemeral.Inspect(context.Background(), cfg, wt, opts); !d.Reap {
		t.Fatalf("precondition: an hour-old clean worktree must be reapable, got %+v", d)
	}
	restore := ephemeral.SetBeforeRemovalForTest(func(string) {
		if err := ephemeral.TouchOpened(wt, time.Now()); err != nil {
			t.Errorf("TouchOpened: %v", err)
		}
	})
	defer restore()
	d, err := ephemeral.ReapWith(context.Background(), cfg, wt, opts)
	if err == nil || !strings.Contains(err.Error(), "opened again before removal") {
		t.Fatalf("err = %v, want an 'opened again before removal' refusal", err)
	}
	if d.Reap {
		t.Errorf("decision must not say reap: %+v", d)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Error("the worktree must survive:", err)
	}
}

// Deleting the marker promotes a worktree to permanent; doing so while a
// reap is in flight must still win.
func TestReapRefusesWhenMarkerRemovedBeforeRemoval(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/promoted")
	restore := ephemeral.SetBeforeRemovalForTest(func(string) {
		if err := os.Remove(filepath.Join(wt, ephemeral.MarkerName)); err != nil {
			t.Errorf("remove marker: %v", err)
		}
	})
	defer restore()
	if _, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()}); err == nil {
		t.Fatal("ReapWith must refuse once the marker is gone")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Error("the worktree must survive:", err)
	}
}

// startIdlessClaude runs a sleeper binary named `claude`, with no session id
// on its command line, in dir, and returns its pid.
func startIdlessClaude(t *testing.T, dir string) int {
	t.Helper()
	build := t.TempDir()
	src := filepath.Join(build, "main.go")
	program := "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Minute) }\n"
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(build, "claude")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Dir = build
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	c := exec.Command(bin)
	c.Dir = dir
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	return c.Process.Pid
}

func TestBlockersRefusesIdlessClaudeProcess(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/idless-claude")
	startIdlessClaude(t, wt)
	waitForBlocker(t, func() []string {
		return ephemeral.Blockers(context.Background(), cfg, wt)
	}, "has a live Claude session")
	// An id-less process can never match the ending session's id.
	b := ephemeral.BlockersExcluding(context.Background(), cfg, wt, "11111111-1111-1111-1111-111111111111")
	if !blockersContain(b, "has a live Claude session") {
		t.Errorf("an id-less claude process must not be exempt by session id, got %v", b)
	}
}

// The SessionEnd hook runs as a descendant of the session that is ending,
// which for a plain `claude` has no id on its command line. That ancestor,
// and only it, is exempt -- and only in hook mode.
func TestBlockersExemptsIdlessClaudeOnlyAsTheHooksAncestor(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/idless-ancestor")
	pid := startIdlessClaude(t, wt)
	waitForBlocker(t, func() []string {
		return ephemeral.Blockers(context.Background(), cfg, wt)
	}, "has a live Claude session")

	t.Cleanup(ephemeral.SetAncestorChainForTest(func(context.Context) ([]model.Proc, error) {
		return []model.Proc{{PID: 999999, Command: "/bin/sh -c wt reap"}, {PID: pid, Command: "claude"}}, nil
	}))
	const ending = "11111111-1111-1111-1111-111111111111"
	if b := ephemeral.BlockersExcluding(context.Background(), cfg, wt, ending); blockersContain(b, "live Claude session") {
		t.Errorf("the hook's own ancestor session must be exempt, got %v", b)
	}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); !blockersContain(b, "live Claude session") {
		t.Errorf("without an ending session nothing is exempt, got %v", b)
	}

	ephemeral.SetAncestorChainForTest(func(context.Context) ([]model.Proc, error) {
		return nil, errors.New("ps failed")
	})
	if b := ephemeral.BlockersExcluding(context.Background(), cfg, wt, ending); !blockersContain(b, "live Claude session") {
		t.Errorf("a failed ancestor walk must exempt nothing, got %v", b)
	}
}

// Only the NEAREST Claude ancestor is the session that is ending. An outer
// interactive `claude` whose Bash tool ran an inner `claude -p` in the same
// worktree is still alive when the inner one's hook fires, and must block.
func TestBlockersExemptsOnlyTheNearestClaudeAncestor(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/nested-claude")
	outer := startIdlessClaude(t, wt)
	inner := startIdlessClaude(t, wt)
	t.Cleanup(ephemeral.SetAncestorChainForTest(func(context.Context) ([]model.Proc, error) {
		return []model.Proc{
			{PID: 999998, Command: "/bin/sh -c wt reap"},
			{PID: inner, Command: "claude -p fix the tests"},
			{PID: 999999, Command: "/bin/zsh -c claude -p fix the tests"},
			{PID: outer, Command: "claude"},
		}, nil
	}))
	const ending = "11111111-1111-1111-1111-111111111111"
	var b []string
	deadline := time.Now().Add(30 * time.Second)
	for {
		b = ephemeral.BlockersExcluding(context.Background(), cfg, wt, ending)
		if blockersContain(b, "live Claude session") || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !blockersContain(b, "live Claude session") {
		t.Fatalf("the outer claude ancestor must still block, got %v", b)
	}
	// And it is the outer one, not the inner one, that blocks.
	d, _ := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{ExcludeSessionID: ending, Now: time.Now()})
	if d.Reap {
		t.Fatalf("ReapWith must refuse: %+v", d)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Error("the worktree must survive:", err)
	}
	if got := ephemeral.LiveSessionPIDsForTest(context.Background(), wt, ending); len(got) != 1 || got[0] != outer {
		t.Errorf("live session pids = %v, want only the outer %d (inner %d is the ending session)", got, outer, inner)
	}
}

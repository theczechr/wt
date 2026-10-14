package ephemeral_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
	"github.com/theczechr/ephemera/internal/trash"
)

// Regression fixture for "working trees containing submodules cannot be
// moved or removed": every clean server ephemeral failed this way.
func TestReapRemovesACleanSubmoduleWorktree(t *testing.T) {
	r := gittest.New(t)
	r.AddSubmodule(t)
	cfg, wt := newEphemeral(t, r, "feat/with-sub")
	r.Git(t, wt, "submodule", "update", "--init", "-q")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("the submodule worktree must be gone")
	}
}

func TestReapRefusesUntrackedFileInsideSubmodule(t *testing.T) {
	r := gittest.New(t)
	r.AddSubmodule(t)
	cfg, wt := newEphemeral(t, r, "feat/sub-dirty")
	r.Git(t, wt, "submodule", "update", "--init", "-q")
	if err := os.WriteFile(filepath.Join(wt, "sub", "scratch.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err == nil {
		t.Fatal("an untracked file inside a submodule must block removal")
	}
	if _, err := os.Stat(filepath.Join(wt, "sub", "scratch.txt")); err != nil {
		t.Error("the file must survive:", err)
	}
}

func markWTBranch(t *testing.T, wt string) {
	t.Helper()
	if err := ephemeral.UpdateMarker(wt, func(m *ephemeral.Marker) { m.BranchCreatedByWT = true }); err != nil {
		t.Fatal(err)
	}
}

func TestReapDeletesWTBranchFullyOnARemote(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/wt-made")
	markWTBranch(t, wt)
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/wt-made"); err == nil {
		t.Error("a wt-created branch fully on the remote must be deleted")
	}
	if e := trash.Load(); len(e) != 1 || !e[0].BranchDeleted {
		t.Errorf("trash = %+v, want one entry with BranchDeleted", e)
	}
}

func TestReapKeepsWTBranchWithUnpushedCommits(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/wt-made-ahead")
	markWTBranch(t, wt)
	// Someone else's unpushed commit: not "own", so the worktree is done,
	// but the branch holds work that exists nowhere else.
	r.Git(t, wt, "-c", "user.email=other@test.invalid", "commit", "-q", "--allow-empty", "-m", "theirs")
	d, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()})
	if err != nil {
		t.Fatalf("ReapWith: %v (decision %+v)", err, d)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/wt-made-ahead"); err != nil {
		t.Error("a branch with commits on no remote must be kept")
	}
}

func TestReapNeverDeletesABranchWTDidNotCreate(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/users-own")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/users-own"); err != nil {
		t.Error("a branch wt did not create must survive the reap")
	}
}

// A process that appears after inspect's probes but before removal (the PR
// lookup is a network call in that window) must still block the reap.
func TestReapRefusesWhenAProcessStartsBeforeRemoval(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/busy-late")

	src := filepath.Join(t.TempDir(), "sleeper.go")
	program := "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Minute) }\n"
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	var cmd *exec.Cmd
	restore := ephemeral.SetBeforeRemovalForTest(func(string) {
		cmd = exec.Command("go", "run", src)
		cmd.Dir = wt
		if err := cmd.Start(); err != nil {
			t.Errorf("could not start the child: %v", err)
			return
		}
		waitForBlocker(t, func() []string {
			return ephemeral.Blockers(context.Background(), cfg, wt)
		}, "running process")
	})
	defer restore()
	t.Cleanup(func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	d, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "became busy") {
		t.Fatalf("err = %v, want a 'became busy' refusal", err)
	}
	if d.Reap {
		t.Errorf("decision must not say reap: %+v", d)
	}
	if _, statErr := os.Stat(wt); statErr != nil {
		t.Error("the worktree must still exist:", statErr)
	}
}

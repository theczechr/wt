package ephemeral_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
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

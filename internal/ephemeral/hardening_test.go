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

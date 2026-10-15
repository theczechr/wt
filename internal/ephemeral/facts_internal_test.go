package ephemeral

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/theczechr/ephemera/internal/gittest"
)

func TestIsLockedReportsLockedUnlockedAndUnlisted(t *testing.T) {
	r := gittest.New(t)
	ctx := context.Background()
	wt := filepath.Join(r.Primary, ".worktrees", "lk")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/lk", wt)

	if locked, err := isLocked(ctx, r.Primary, wt); err != nil || locked {
		t.Fatalf("unlocked worktree: locked=%v err=%v, want false <nil>", locked, err)
	}

	r.Git(t, r.Primary, "worktree", "lock", "--reason", "busy", wt)
	if locked, err := isLocked(ctx, r.Primary, wt); err != nil || !locked {
		t.Fatalf("locked worktree: locked=%v err=%v, want true <nil>", locked, err)
	}
	r.Git(t, r.Primary, "worktree", "unlock", wt)

	notAWorktree := filepath.Join(r.Primary, ".worktrees", "nope")
	if locked, err := isLocked(ctx, r.Primary, notAWorktree); err == nil {
		t.Fatalf("path not in worktree list: locked=%v err=nil, want an error (a fact that cannot be read is a blocker, never unlocked)", locked)
	}
}

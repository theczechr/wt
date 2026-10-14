package trash_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/gittest"
	"github.com/theczechr/ephemera/internal/model"
	"github.com/theczechr/ephemera/internal/trash"
)

func cleanWS(path, branch string) model.Workspace {
	return model.Workspace{Repo: "demo", Path: path, Branch: branch, StatusKnown: true, SessionsKnown: true}
}

func TestSoftDeleteWithForceRemovesSubmoduleWorktree(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.AddSubmodule(t)
	wt := filepath.Join(r.Primary, ".worktrees", "sub-wt")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/sub", wt)
	r.Git(t, wt, "submodule", "update", "--init", "-q")

	if _, err := trash.SoftDelete(context.Background(), cleanWS(wt, "feat/sub"), r.Primary); err == nil {
		t.Fatal("precondition: plain removal must fail on a submodule worktree (git refuses)")
	}
	if _, err := trash.SoftDeleteWith(context.Background(), cleanWS(wt, "feat/sub"), r.Primary,
		trash.RemoveOptions{Force: true}); err != nil {
		t.Fatalf("SoftDeleteWith(Force): %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("the worktree must be gone")
	}
}

func TestSoftDeleteWithForceStillRefusesALockedWorktree(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	wt := filepath.Join(r.Primary, ".worktrees", "locked")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/locked", wt)
	r.Git(t, r.Primary, "worktree", "lock", wt)
	if _, err := trash.SoftDeleteWith(context.Background(), cleanWS(wt, "feat/locked"), r.Primary,
		trash.RemoveOptions{Force: true}); err == nil {
		t.Fatal("one --force must never override a lock")
	}
}

func TestSoftDeleteWithDeleteBranchRecordsIt(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	wt := filepath.Join(r.Primary, ".worktrees", "pr-9")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "pr/9", wt)
	e, err := trash.SoftDeleteWith(context.Background(), cleanWS(wt, "pr/9"), r.Primary,
		trash.RemoveOptions{DeleteBranch: true, PR: 9})
	if err != nil {
		t.Fatal(err)
	}
	if !e.BranchDeleted || e.PR != 9 {
		t.Errorf("entry = %+v, want BranchDeleted and PR 9", e)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/pr/9"); err == nil {
		t.Error("the branch must be deleted")
	}
}

func TestRestoreRefetchesADeletedPRBranch(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	// A PR head that exists only as refs/pull/9/head on origin.
	r.Git(t, r.Origin, "checkout", "-q", "-b", "tmp-fork")
	r.WriteCommit(t, r.Origin, "fork.txt", "x")
	sha := strings.TrimSpace(r.Git(t, r.Origin, "rev-parse", "HEAD"))
	r.Git(t, r.Origin, "update-ref", "refs/pull/9/head", sha)
	r.Git(t, r.Origin, "checkout", "-q", "main")
	r.Git(t, r.Origin, "branch", "-qD", "tmp-fork")

	wt := filepath.Join(r.Primary, ".worktrees", "pr-9")
	e := trash.Entry{Path: wt, Repo: "demo", Branch: "pr/9", Primary: r.Primary, PR: 9, BranchDeleted: true}
	if err := trash.Restore(context.Background(), e, config.Repo{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := strings.TrimSpace(r.Git(t, wt, "rev-parse", "HEAD")); got != sha {
		t.Errorf("restored HEAD = %s, want the PR head %s", got, sha)
	}
}

func TestRestoreOntoExistingPRBranchSucceeds(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.Git(t, r.Origin, "update-ref", "refs/pull/9/head", "main")
	r.Git(t, r.Primary, "branch", "pr/9", "origin/main")

	wt := filepath.Join(r.Primary, ".worktrees", "pr-9")
	e := trash.Entry{Path: wt, Repo: "demo", Branch: "pr/9", Primary: r.Primary, PR: 9, BranchDeleted: true}
	if err := trash.Restore(context.Background(), e, config.Repo{}); err != nil {
		t.Fatalf("Restore onto an existing branch: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree must exist: %v", err)
	}
}

func TestRestoreIsRetryableOnceTheBranchExists(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.Git(t, r.Origin, "update-ref", "refs/pull/9/head", "main")

	wt := filepath.Join(r.Primary, ".worktrees", "pr-9")
	e := trash.Entry{Path: wt, Repo: "demo", Branch: "pr/9", Primary: r.Primary, PR: 9, BranchDeleted: true}
	if err := trash.Restore(context.Background(), e, config.Repo{}); err != nil {
		t.Fatalf("first Restore: %v", err)
	}
	// Simulate a worktree that was removed again, with the branch left behind
	// (the state a failed worktree add leaves). The second Restore must work.
	r.Git(t, r.Primary, "worktree", "remove", wt)
	if err := trash.Restore(context.Background(), e, config.Repo{}); err != nil {
		t.Fatalf("second Restore must succeed with the branch already present: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree must exist after the second Restore: %v", err)
	}
}

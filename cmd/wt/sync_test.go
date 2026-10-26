package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
)

// pushUpstream lands a commit on branch in origin, as a teammate pushing
// would, without the primary hearing about it.
func pushUpstream(t *testing.T, r *gittest.Repo, branch, file string) string {
	t.Helper()
	r.Git(t, r.Origin, "checkout", "-q", branch)
	r.WriteCommit(t, r.Origin, file, "upstream")
	sha := strings.TrimSpace(r.Git(t, r.Origin, "rev-parse", "HEAD"))
	r.Git(t, r.Origin, "checkout", "-q", "main")
	return sha
}

func head(t *testing.T, r *gittest.Repo, dir string) string {
	t.Helper()
	return strings.TrimSpace(r.Git(t, dir, "rev-parse", "HEAD"))
}

func TestOpenBranchRemoteOnlyStartsFromTheCurrentRemoteTip(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/stale")
	want := pushUpstream(t, r, "feat/stale", "later.txt")

	path, err := openBranch(cfgFor(r, config.Repo{}), "feat/stale")
	if err != nil {
		t.Fatal(err)
	}
	if got := head(t, r, path); got != want {
		t.Errorf("HEAD = %s, want the remote's current tip %s, not the stale tracking ref", got, want)
	}
}

func TestOpenBranchFastForwardsAStaleLocalBranchBeforeCheckout(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/local")
	r.Git(t, r.Primary, "branch", "feat/local", "origin/feat/local")
	want := pushUpstream(t, r, "feat/local", "later.txt")

	path, err := openBranch(cfgFor(r, config.Repo{}), "feat/local")
	if err != nil {
		t.Fatal(err)
	}
	if got := head(t, r, path); got != want {
		t.Errorf("HEAD = %s, want fast-forwarded to %s", got, want)
	}
	m, err := ephemeral.ReadMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Start != want {
		t.Errorf("marker start = %s, want %s: pulled commits are not the user's", m.Start, want)
	}
}

func TestOpenBranchAgainFastForwardsTheExistingWorktree(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/again")
	cfg := cfgFor(r, config.Repo{})
	path, err := openBranch(cfg, "feat/again")
	if err != nil {
		t.Fatal(err)
	}
	// Uncommitted work that the incoming commit does not touch rides along.
	if err := os.WriteFile(filepath.Join(path, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := pushUpstream(t, r, "feat/again", "later.txt")

	if _, err := openBranch(cfg, "feat/again"); err != nil {
		t.Fatal(err)
	}
	if got := head(t, r, path); got != want {
		t.Errorf("HEAD = %s, want fast-forwarded to %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(path, "notes.txt")); err != nil {
		t.Errorf("uncommitted file lost: %v", err)
	}
	m, err := ephemeral.ReadMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Start != want {
		t.Errorf("marker start = %s, want advanced to %s", m.Start, want)
	}
}

func TestOpenBranchAgainLeavesADivergedWorktreeAlone(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/mine")
	cfg := cfgFor(r, config.Repo{})
	path, err := openBranch(cfg, "feat/mine")
	if err != nil {
		t.Fatal(err)
	}
	r.WriteCommit(t, path, "local.txt", "mine")
	own := head(t, r, path)
	pushUpstream(t, r, "feat/mine", "later.txt")

	if _, err := openBranch(cfg, "feat/mine"); err != nil {
		t.Fatal(err)
	}
	if got := head(t, r, path); got != own {
		t.Errorf("HEAD moved to %s; a branch with its own commits must be left alone", got)
	}
}

func TestOpenBranchAgainKeepsAConflictingUncommittedChange(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/dirty")
	cfg := cfgFor(r, config.Repo{})
	path, err := openBranch(cfg, "feat/dirty")
	if err != nil {
		t.Fatal(err)
	}
	before := head(t, r, path)
	// Untracked, and the incoming commit adds the same path: a fast-forward
	// would overwrite it, so git must refuse and wt must not force it.
	if err := os.WriteFile(filepath.Join(path, "later.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	pushUpstream(t, r, "feat/dirty", "later.txt")

	if _, err := openBranch(cfg, "feat/dirty"); err != nil {
		t.Fatal(err)
	}
	if got := head(t, r, path); got != before {
		t.Errorf("HEAD moved to %s over a conflicting local file", got)
	}
	if b, err := os.ReadFile(filepath.Join(path, "later.txt")); err != nil || string(b) != "mine" {
		t.Errorf("local later.txt = %q, %v; want it untouched", b, err)
	}
}

func TestOpenBranchFetchesUnderASingleBranchRefspec(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/narrow")
	r.Git(t, r.Primary, "config", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/main")
	want := pushUpstream(t, r, "feat/narrow", "later.txt")

	path, err := openBranch(cfgFor(r, config.Repo{}), "feat/narrow")
	if err != nil {
		t.Fatal(err)
	}
	if got := head(t, r, path); got != want {
		t.Errorf("HEAD = %s, want %s: the refresh must not depend on remote.origin.fetch", got, want)
	}
}

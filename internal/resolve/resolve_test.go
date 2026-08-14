package resolve_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/gittest"
	"github.com/theczechr/wt/internal/resolve"
)

func TestFindLocatesALocalBranch(t *testing.T) {
	r := gittest.New(t)
	r.Git(t, r.Primary, "branch", "feat/local")
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	got := resolve.Find(context.Background(), cfg,
		map[string]string{"demo": r.Primary}, "feat/local")
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(got), got)
	}
	if !got[0].Local || got[0].Repo != "demo" {
		t.Errorf("got %+v, want a local match in demo", got[0])
	}
}

func TestFindLocatesARemoteOnlyBranch(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/remote-only")
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	got := resolve.Find(context.Background(), cfg,
		map[string]string{"demo": r.Primary}, "feat/remote-only")
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1", len(got))
	}
	if got[0].Local {
		t.Error("must not report a local ref that does not exist")
	}
	if got[0].Remote != "origin" {
		t.Errorf("Remote = %q, want origin", got[0].Remote)
	}
}

func TestFindReportsEveryRepoHoldingTheBranch(t *testing.T) {
	a, b := gittest.New(t), gittest.New(t)
	a.Git(t, a.Primary, "branch", "shared")
	b.Git(t, b.Primary, "branch", "shared")
	cfg := config.Config{Repos: map[string]config.Repo{"a": {Name: "a"}, "b": {Name: "b"}}}
	got := resolve.Find(context.Background(), cfg,
		map[string]string{"a": a.Primary, "b": b.Primary}, "shared")
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(got), got)
	}
}

func TestFindReportsAnExistingWorktreeForTheBranch(t *testing.T) {
	r := gittest.New(t)
	wt := filepath.Join(filepath.Dir(r.Primary), "checked-out")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/busy", wt)
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	got := resolve.Find(context.Background(), cfg,
		map[string]string{"demo": r.Primary}, "feat/busy")
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1", len(got))
	}
	if got[0].ExistingWorktree == "" {
		t.Fatal("must report the worktree already holding the branch")
	}
	if filepath.Base(got[0].ExistingWorktree) != "checked-out" {
		t.Errorf("ExistingWorktree = %q", got[0].ExistingWorktree)
	}
}

func TestFindReturnsNothingForAnUnknownBranch(t *testing.T) {
	r := gittest.New(t)
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	got := resolve.Find(context.Background(), cfg,
		map[string]string{"demo": r.Primary}, "no/such/branch")
	if len(got) != 0 {
		t.Fatalf("got %+v, want no matches", got)
	}
}

func TestFetchMakesANewOriginBranchVisible(t *testing.T) {
	r := gittest.New(t)
	// Push a branch straight into origin so the clone has never seen it.
	r.Git(t, r.Origin, "branch", "feat/fresh")
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	primaries := map[string]string{"demo": r.Primary}
	ctx := context.Background()
	if got := resolve.Find(ctx, cfg, primaries, "feat/fresh"); len(got) != 0 {
		t.Fatal("precondition: the clone must not know the branch yet")
	}
	resolve.Fetch(ctx, primaries)
	if got := resolve.Find(ctx, cfg, primaries, "feat/fresh"); len(got) != 1 {
		t.Fatalf("after Fetch, got %d matches, want 1", len(got))
	}
}

// TestFetchUpdatesEveryRemoteNotJustTheDefault pins Fetch to what Find
// actually searches. findIn builds a pattern for every remote `git remote`
// reports, but Fetch used to run `git fetch --prune <default_remote>` only --
// so a branch living on a fork's `upstream` was searchable and never
// fetchable, and the miss-then-fetch-then-retry path reported "not found ...
// (after fetching)": true, and misleading.
//
// The fixture is the fork workflow exactly: origin has nothing, a second
// remote named upstream carries the branch, and default_remote stays at its
// "origin" default -- so a default-only fetch leaves the branch invisible.
func TestFetchUpdatesEveryRemoteNotJustTheDefault(t *testing.T) {
	r := gittest.New(t)
	fork := gittest.New(t)
	r.Git(t, r.Primary, "remote", "add", "upstream", fork.Origin)
	fork.Git(t, fork.Origin, "branch", "feat/only-upstream")

	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	primaries := map[string]string{"demo": r.Primary}
	ctx := context.Background()
	if got := resolve.Find(ctx, cfg, primaries, "feat/only-upstream"); len(got) != 0 {
		t.Fatal("precondition: the clone must not know the branch yet")
	}

	resolve.Fetch(ctx, primaries)

	got := resolve.Find(ctx, cfg, primaries, "feat/only-upstream")
	if len(got) != 1 {
		t.Fatalf("after Fetch, got %d matches, want 1 -- a non-default remote must be fetched too", len(got))
	}
	if got[0].Remote != "upstream" {
		t.Errorf("Remote = %q, want upstream", got[0].Remote)
	}
}

func TestValidBranchNameRejectsDangerousInput(t *testing.T) {
	for _, bad := range []string{"", "-rf", "--upload-pack=x", "has space",
		"ends.lock", "a..b", "~tilde", "back\\slash"} {
		if err := resolve.ValidBranchName(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	for _, ok := range []string{"main", "feat/x", "fix/UP-123-thing", "v1.2.3"} {
		if err := resolve.ValidBranchName(ok); err != nil {
			t.Errorf("%q must be accepted: %v", ok, err)
		}
	}
}

package discover_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestUnpushedOrphans(t *testing.T) {
	r := gittest.New(t)
	// local-only work, no worktree: listed
	r.Git(t, r.Primary, "checkout", "-q", "-b", "feat/orphan")
	r.WriteCommit(t, r.Primary, "o.txt", "x")
	r.Git(t, r.Primary, "checkout", "-q", "main")
	// pushed, no worktree: not listed
	r.PushNewBranch(t, "feat/pushed")
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	r.Git(t, r.Primary, "branch", "-q", "feat/pushed", "origin/feat/pushed")
	// local-only work, checked out in a worktree: not listed
	wt := filepath.Join(r.Primary, ".worktrees", "busy")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/busy", wt)
	r.WriteCommit(t, wt, "b.txt", "x")

	got, err := discover.UnpushedOrphans(context.Background(), r.Primary)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"feat/orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A tag sharing a branch's name makes %(refname:short) disambiguate to
// "heads/<name>"; branch names must come out unchanged regardless.
func TestUnpushedOrphansWithATagNamedLikeABranch(t *testing.T) {
	r := gittest.New(t)
	r.Git(t, r.Primary, "checkout", "-q", "-b", "feat/orphan")
	r.WriteCommit(t, r.Primary, "o.txt", "x")
	r.Git(t, r.Primary, "tag", "feat/orphan")
	r.Git(t, r.Primary, "checkout", "-q", "main")
	wt := filepath.Join(r.Primary, ".worktrees", "busy")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/busy", wt)
	r.WriteCommit(t, wt, "b.txt", "x")
	r.Git(t, r.Primary, "tag", "feat/busy", "feat/busy")

	got, err := discover.UnpushedOrphans(context.Background(), r.Primary)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"feat/orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

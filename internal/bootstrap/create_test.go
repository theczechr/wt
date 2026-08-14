package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/bootstrap"
	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/gittest"
)

func TestCreateAtChecksOutFromTheStartPointForARemoteOnlyBranch(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/pr")
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	target := filepath.Join(r.Primary, ".worktrees", "pr")

	if err := bootstrap.CreateAt(context.Background(), r.Primary, target,
		"feat/pr", "origin/feat/pr", config.Repo{}); err != nil {
		t.Fatal(err)
	}

	want := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "origin/feat/pr"))
	got := strings.TrimSpace(r.Git(t, target, "rev-parse", "HEAD"))
	if got != want {
		t.Errorf("HEAD = %s, want origin/feat/pr (%s): the worktree must hold the PR's code, not an empty branch off HEAD", got, want)
	}
	primaryHead := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "HEAD"))
	if got == primaryHead {
		t.Error("HEAD must not equal the primary's HEAD")
	}
}

func TestCreateAtTracksTheRemoteBranch(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/tracked")
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	target := filepath.Join(r.Primary, ".worktrees", "tracked")
	if err := bootstrap.CreateAt(context.Background(), r.Primary, target,
		"feat/tracked", "origin/feat/tracked", config.Repo{}); err != nil {
		t.Fatal(err)
	}
	// Upstream must exist, or the reap predicate will always refuse this
	// worktree -- a branch with no upstream is treated as fully unpushed.
	up := strings.TrimSpace(r.Git(t, target, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"))
	if up != "origin/feat/tracked" {
		t.Errorf("upstream = %q, want origin/feat/tracked", up)
	}
}

func TestCreateAtUsesTheExistingLocalBranch(t *testing.T) {
	r := gittest.New(t)
	r.Git(t, r.Primary, "branch", "feat/local")
	target := filepath.Join(r.Primary, ".worktrees", "local")
	if err := bootstrap.CreateAt(context.Background(), r.Primary, target,
		"feat/local", "", config.Repo{}); err != nil {
		t.Fatal(err)
	}
	br := strings.TrimSpace(r.Git(t, target, "rev-parse", "--abbrev-ref", "HEAD"))
	if br != "feat/local" {
		t.Errorf("branch = %q, want feat/local", br)
	}
}

func TestCreateAtRefusesAnExistingTarget(t *testing.T) {
	r := gittest.New(t)
	target := filepath.Join(r.Primary, ".worktrees", "taken")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.CreateAt(context.Background(), r.Primary, target,
		"feat/x", "", config.Repo{}); err == nil {
		t.Error("must refuse rather than invent a variant name")
	}
}

func TestCreateAtRefusesAnEmptyPrimary(t *testing.T) {
	if err := bootstrap.CreateAt(context.Background(), "", "/tmp/x", "b", "", config.Repo{}); err == nil {
		t.Error("an empty primary must be refused: git -C '' operates on the caller's cwd")
	}
}

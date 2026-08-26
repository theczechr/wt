package resolve

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/theczechr/wt/internal/config"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// repoOnOddBranch builds a repo whose checked-out branch is deliberately NOT
// a mainline, mirroring the real situation: a primary checkout sits on
// whatever was last worked on there.
func repoOnOddBranch(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "master", dir)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "-C", dir, "add", "f")
	git(t, dir, "-C", dir, "commit", "-qm", "init")
	git(t, dir, "-C", dir, "checkout", "-q", "-b", "hotfix/unrelated")
	return dir
}

// TestBaseUsesConfiguredBranch: explicit beats inferred.
func TestBaseUsesConfiguredBranch(t *testing.T) {
	dir := repoOnOddBranch(t)
	ref, guessed := Base(context.Background(), config.Repo{Base: "master"}, dir)
	if guessed {
		t.Error("a configured base was reported as a guess")
	}
	if ref != "master" {
		t.Errorf("ref = %q, want master", ref)
	}
}

// TestBaseFallsBackToPrimaryHeadAndSaysSo is the case that must never be
// silent. With no configured base and no remote default there is nothing to
// infer from, so it guesses -- and cutting a branch from whatever the
// primary happens to be on is exactly the trap this function exists to
// avoid. The caller has to be able to tell the user.
func TestBaseFallsBackToPrimaryHeadAndSaysSo(t *testing.T) {
	dir := repoOnOddBranch(t) // no remotes at all
	ref, guessed := Base(context.Background(), config.Repo{}, dir)
	if !guessed {
		t.Fatal("a fallback to the primary's HEAD was not reported as a guess")
	}
	if ref != "HEAD" {
		t.Errorf("ref = %q, want HEAD", ref)
	}
	if d := DescribeBase(ref, guessed); d == ref {
		t.Error("DescribeBase did not explain the guess")
	}
}

// TestDescribeBaseIsPlainWhenCertain keeps the explanation out of the way
// when there is nothing to warn about.
func TestDescribeBaseIsPlainWhenCertain(t *testing.T) {
	if got := DescribeBase("origin/develop", false); got != "origin/develop" {
		t.Errorf("DescribeBase = %q, want the bare ref", got)
	}
}

package gittest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/gittest"
)

func TestNewProducesAClonedPrimaryWithAnOrigin(t *testing.T) {
	r := gittest.New(t)
	out := r.Git(t, r.Primary, "remote")
	if strings.TrimSpace(out) != "origin" {
		t.Errorf("remote = %q, want origin", out)
	}
	head := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "HEAD"))
	if len(head) != 40 {
		t.Errorf("HEAD = %q, want a full sha", head)
	}
}

func TestPushNewBranchIsVisibleAsARemoteRef(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/remote-only")
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet",
		"refs/remotes/origin/feat/remote-only"); err != nil {
		t.Fatal("origin/feat/remote-only must exist after PushNewBranch:", err)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet",
		"refs/heads/feat/remote-only"); err == nil {
		t.Fatal("PushNewBranch must leave no local branch behind")
	}
}

func TestWriteCommitAdvancesHead(t *testing.T) {
	r := gittest.New(t)
	before := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "HEAD"))
	r.WriteCommit(t, r.Primary, "extra.txt", "hello")
	after := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "HEAD"))
	if before == after {
		t.Error("WriteCommit must create a commit")
	}
	if _, err := os.Stat(filepath.Join(r.Primary, "extra.txt")); err != nil {
		t.Error("WriteCommit must write the file:", err)
	}
}

func TestAmbientGlobalConfigDoesNotLeakIn(t *testing.T) {
	// The helper's isolation guarantee is what lets later safety tests trust
	// their results on any machine. Prove it holds rather than assuming it.
	// Use a pre-commit hook that fails if isolation doesn't work; without
	// GIT_CONFIG_GLOBAL isolation, the hook would run and prevent commits.
	home := t.TempDir()
	hooksDir := filepath.Join(home, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte(fmt.Sprintf("[core]\n\thooksPath = %s\n", hooksDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	r := gittest.New(t)
	// If isolation fails, this WriteCommit would fail because the hook runs.
	// If isolation works, the hook doesn't run and WriteCommit succeeds.
	r.WriteCommit(t, r.Primary, "test.txt", "content")
}

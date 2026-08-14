package ephemeral_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/ephemeral"
	"github.com/theczechr/wt/internal/gittest"
)

func TestSweepRemovesOnlyTheReapableOne(t *testing.T) {
	r := gittest.New(t)
	cfg, clean := newEphemeral(t, r, "feat/sweepable")
	_, dirty := newEphemeral(t, r, "feat/keepme")
	if err := os.WriteFile(filepath.Join(dirty, "work.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed := ephemeral.Sweep(context.Background(), cfg, map[string]string{"demo": r.Primary})

	if len(removed) != 1 {
		t.Fatalf("removed %v, want exactly the clean worktree", removed)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Error("the clean worktree must be gone")
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Error("the dirty worktree must survive:", err)
	}
}

func TestSweepIgnoresUnmarkedDirectories(t *testing.T) {
	r := gittest.New(t)
	cfg, _ := newEphemeral(t, r, "feat/marked")
	stray := filepath.Join(r.Primary, ".worktrees", "not-ours")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ephemeral.Sweep(context.Background(), cfg, map[string]string{"demo": r.Primary})
	if _, err := os.Stat(stray); err != nil {
		t.Error("a directory without a marker must be left alone:", err)
	}
}

func TestSweepOnAMissingEphemeralDirIsNotAnError(t *testing.T) {
	r := gittest.New(t)
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	if got := ephemeral.Sweep(context.Background(), cfg, map[string]string{"demo": r.Primary}); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

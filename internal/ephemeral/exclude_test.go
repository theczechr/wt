package ephemeral_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/ephemeral"
	"github.com/theczechr/wt/internal/gittest"
)

func TestEnsureExcludedMakesTheMarkerInvisibleToStatus(t *testing.T) {
	r := gittest.New(t)
	if err := ephemeral.EnsureExcluded(context.Background(), r.Primary,
		[]string{".worktrees/", ephemeral.MarkerName}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Primary, ephemeral.MarkerName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := r.Git(t, r.Primary, "status", "--porcelain")
	if strings.TrimSpace(out) != "" {
		t.Errorf("status must be clean after excluding the marker, got:\n%s", out)
	}
}

func TestEnsureExcludedIsIdempotent(t *testing.T) {
	r := gittest.New(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := ephemeral.EnsureExcluded(ctx, r.Primary, []string{".worktrees/"}); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(r.Primary, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), ".worktrees/"); n != 1 {
		t.Errorf("wrote %d copies of the line, want 1", n)
	}
}

func TestEnsureExcludedPreservesExistingContent(t *testing.T) {
	r := gittest.New(t)
	excl := filepath.Join(r.Primary, ".git", "info", "exclude")
	if err := os.WriteFile(excl, []byte("# mine\nscratch/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.EnsureExcluded(context.Background(), r.Primary, []string{".worktrees/"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(excl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "scratch/") {
		t.Error("must not clobber pre-existing exclude entries")
	}
}

func TestEnsureExcludedResolvesCommonDirFromAWorktree(t *testing.T) {
	r := gittest.New(t)
	wt := filepath.Join(r.Primary, ".worktrees", "probe")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "probe", wt)
	// Called with the WORKTREE's path, not the primary's: a worktree's .git
	// is a file, so the exclude file must still be found via the common dir.
	if err := ephemeral.EnsureExcluded(context.Background(), wt, []string{".worktrees/"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(r.Primary, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), ".worktrees/") {
		t.Error("must write to the common dir's exclude file")
	}
}

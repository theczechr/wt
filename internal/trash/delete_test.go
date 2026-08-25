package trash

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/model"
)

// runGit runs git against dir with a fixed, hermetic author/committer
// identity (mirrors internal/discover/gitstatus_test.go's helper), so these
// tests never depend on the machine's global git config.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wt-test", "GIT_AUTHOR_EMAIL=wt-test@example.com",
		"GIT_COMMITTER_NAME=wt-test", "GIT_COMMITTER_EMAIL=wt-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// newPrimaryRepo creates a throwaway git repo with one commit on "main" and
// returns its path. Every test in this file builds its own repo in
// t.TempDir() -- never touches a real checkout.
func newPrimaryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func addWorktree(t *testing.T, primary, target, branch string) {
	t.Helper()
	runGit(t, primary, "worktree", "add", "-q", target, "-b", branch)
}

func branchExists(t *testing.T, primary, branch string) bool {
	t.Helper()
	err := exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()
	return err == nil
}

func worktreeListed(t *testing.T, primary, path string) bool {
	t.Helper()
	out := runGit(t, primary, "worktree", "list", "--porcelain")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	return strings.Contains(out, path) || strings.Contains(out, resolved)
}

// TestSoftDeleteDoesNotFollowSymlinkOutOfWorktree is the non-negotiable
// safety test this feature exists to prove: a worktree containing a symlink
// pointing OUTSIDE itself (exactly the shape of a `wt bootstrap`-created
// .env symlink, which points back into the primary checkout) must survive
// SoftDelete with its target untouched. SoftDelete's only deletion
// mechanism is `git worktree remove` (see delete.go) -- like os.RemoveAll,
// this unlinks a symlink it encounters rather than following it, but that
// must be demonstrated, not assumed: a regression that swapped in a
// symlink-following removal (e.g. shelling out to `rm -rf` through a
// symlinked directory, or resolving the symlink before deleting) would
// destroy the sentinel file and this test would catch it.
func TestSoftDeleteDoesNotFollowSymlinkOutOfWorktree(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())

	primary := newPrimaryRepo(t)

	sentinelDir := t.TempDir()
	sentinelPath := filepath.Join(sentinelDir, "real-secret.env")
	const sentinelBody = "SECRET=do-not-delete-me"
	if err := os.WriteFile(sentinelPath, []byte(sentinelBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// .gitignore must already be tracked in primary's history BEFORE the
	// worktree is created: a .gitignore that is itself untracked would make
	// the worktree "dirty" by git's own reckoning (an untracked file),
	// which is a real and correct refusal but not what this test is
	// probing -- it wants a CLEAN worktree containing an ignored symlink,
	// matching exactly how `wt bootstrap`'s real .env symlinks are always
	// gitignored ahead of time.
	if err := os.WriteFile(filepath.Join(primary, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, primary, "add", ".gitignore")
	runGit(t, primary, "commit", "-q", "-m", "ignore .env")

	worktreePath := filepath.Join(filepath.Dir(primary), "wt-symlink-test")
	addWorktree(t, primary, worktreePath, "feature/symlink-test")

	// A symlink inside the worktree pointing at the sentinel file outside
	// it -- the exact shape of a bootstrapped .env.
	envLink := filepath.Join(worktreePath, ".env")
	if err := os.Symlink(sentinelPath, envLink); err != nil {
		t.Fatal(err)
	}

	ws := model.Workspace{
		Repo: "test", Path: worktreePath, Branch: "feature/symlink-test",
		Kind: model.KindSibling, StatusKnown: true, SessionsKnown: true,
	}
	if _, err := SoftDelete(context.Background(), ws, primary); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree directory must be gone, stat err = %v", err)
	}

	// The critical assertion: the sentinel file OUTSIDE the worktree must
	// still exist, byte-identical.
	body, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("sentinel file must still exist: %v", err)
	}
	if string(body) != sentinelBody {
		t.Errorf("sentinel content = %q, want %q (must survive untouched)", body, sentinelBody)
	}
}

func TestSoftDeleteRemovesWorktreeKeepsBranchWritesManifest(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	primary := newPrimaryRepo(t)
	target := filepath.Join(filepath.Dir(primary), "wt-soft-delete-test")
	addWorktree(t, primary, target, "feature/soft-delete")

	ws := model.Workspace{Repo: "test", Path: target, Branch: "feature/soft-delete", Kind: model.KindSibling, StatusKnown: true, SessionsKnown: true}
	entry, err := SoftDelete(context.Background(), ws, primary)
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	if worktreeListed(t, primary, target) {
		t.Error("worktree must be gone from `git worktree list`")
	}
	if !branchExists(t, primary, "feature/soft-delete") {
		t.Error("branch must survive a soft delete")
	}

	manifest := Load()
	if len(manifest) != 1 {
		t.Fatalf("manifest has %d entries, want 1", len(manifest))
	}
	if manifest[0].Path != target || manifest[0].Branch != "feature/soft-delete" {
		t.Errorf("manifest entry = %+v", manifest[0])
	}
	if entry.Path != target {
		t.Errorf("returned entry.Path = %q, want %q", entry.Path, target)
	}
}

func TestSoftDeleteRefusesDirtyWorktreeAndLeavesItIntact(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	primary := newPrimaryRepo(t)
	target := filepath.Join(filepath.Dir(primary), "wt-dirty-test")
	addWorktree(t, primary, target, "feature/dirty")

	ws := model.Workspace{
		Repo: "test", Path: target, Branch: "feature/dirty", Kind: model.KindSibling,
		StatusKnown: true, SessionsKnown: true,
		DirtyCount: 1, // PruneBlockers' own signal, exactly as aggregate.Collect would set it
	}
	_, err := SoftDelete(context.Background(), ws, primary)
	if err == nil {
		t.Fatal("expected SoftDelete to refuse a dirty worktree")
	}
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("expected a *BlockedError, got %T: %v", err, err)
	}
	if !strings.Contains(be.Error(), "uncommitted changes") {
		t.Errorf("blocker reason = %q, want it to mention uncommitted changes", be.Error())
	}

	if !worktreeListed(t, primary, target) {
		t.Error("dirty worktree must still exist after a refused delete")
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("dirty worktree directory must still exist on disk: %v", statErr)
	}
	if len(Load()) != 0 {
		t.Error("a refused delete must not write a manifest entry")
	}
}

func TestHardDeleteOnUnmergedBranchKeepsBranchReportsRefusal(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	primary := newPrimaryRepo(t)
	target := filepath.Join(filepath.Dir(primary), "wt-hard-unmerged-test")
	addWorktree(t, primary, target, "feature/unmerged")

	// A commit on the branch that main never gets: -d must refuse.
	if err := os.WriteFile(filepath.Join(target, "unmerged.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "add", "unmerged.txt")
	runGit(t, target, "commit", "-q", "-m", "unmerged work")

	ws := model.Workspace{Repo: "test", Path: target, Branch: "feature/unmerged", Kind: model.KindSibling, StatusKnown: true, SessionsKnown: true}
	res, err := HardDelete(context.Background(), ws, primary)
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if res.BranchDeleted {
		t.Fatal("branch must NOT be deleted: it is unmerged")
	}
	if res.BranchKeptReason == "" {
		t.Error("expected a non-empty refusal reason")
	}
	if worktreeListed(t, primary, target) {
		t.Error("worktree itself must still be removed")
	}
	if !branchExists(t, primary, "feature/unmerged") {
		t.Error("unmerged branch must survive: HardDelete must never fall back to -D")
	}
	if len(Load()) != 0 {
		t.Error("a hard delete must never write a trash manifest entry")
	}
}

func TestHardDeleteOnMergedBranchDeletesBranch(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	primary := newPrimaryRepo(t)
	target := filepath.Join(filepath.Dir(primary), "wt-hard-merged-test")
	addWorktree(t, primary, target, "feature/merged")
	// No new commits: the branch is identical to main, i.e. already merged.

	ws := model.Workspace{Repo: "test", Path: target, Branch: "feature/merged", Kind: model.KindSibling, StatusKnown: true, SessionsKnown: true}
	res, err := HardDelete(context.Background(), ws, primary)
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if !res.BranchDeleted {
		t.Fatalf("expected the already-merged branch to be deleted, kept reason: %q", res.BranchKeptReason)
	}
	if branchExists(t, primary, "feature/merged") {
		t.Error("merged branch must be gone")
	}
}

func TestHardDeleteRefusesPrimaryCheckout(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	primary := newPrimaryRepo(t)
	ws := model.Workspace{Repo: "test", Path: primary, Branch: "main", Kind: model.KindPrimary}
	_, err := HardDelete(context.Background(), ws, primary)
	if err == nil {
		t.Fatal("expected HardDelete to refuse the primary checkout")
	}
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("expected a *BlockedError, got %T: %v", err, err)
	}
	if _, statErr := os.Stat(primary); statErr != nil {
		t.Fatalf("primary checkout must be untouched: %v", statErr)
	}
}

func TestRestoreRecreatesWorktreeAndRemovesManifestEntry(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	primary := newPrimaryRepo(t)
	target := filepath.Join(filepath.Dir(primary), "wt-restore-test")
	addWorktree(t, primary, target, "feature/restore-me")

	ws := model.Workspace{Repo: "test", Path: target, Branch: "feature/restore-me", Kind: model.KindSibling, StatusKnown: true, SessionsKnown: true}
	entry, err := SoftDelete(context.Background(), ws, primary)
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("sanity: worktree should be gone before Restore, stat err = %v", err)
	}

	if err := Restore(context.Background(), entry, config.Repo{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Errorf("worktree must exist again at its original path: %v", err)
	}
	out := runGit(t, target, "rev-parse", "--abbrev-ref", "HEAD")
	if strings.TrimSpace(out) != "feature/restore-me" {
		t.Errorf("restored worktree branch = %q, want feature/restore-me", strings.TrimSpace(out))
	}
	if len(Load()) != 0 {
		t.Error("manifest entry must be removed after a successful restore")
	}
}

func TestRestoreRefusesWhenPathIsOccupied(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	primary := newPrimaryRepo(t)
	target := filepath.Join(filepath.Dir(primary), "wt-restore-occupied-test")
	addWorktree(t, primary, target, "feature/occupied")

	ws := model.Workspace{Repo: "test", Path: target, Branch: "feature/occupied", Kind: model.KindSibling, StatusKnown: true, SessionsKnown: true}
	entry, err := SoftDelete(context.Background(), ws, primary)
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Something else now occupies the original path.
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "occupant.txt"), []byte("not a worktree"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Restore(context.Background(), entry, config.Repo{}); err == nil {
		t.Fatal("expected Restore to refuse an occupied path")
	}
	body, err := os.ReadFile(filepath.Join(target, "occupant.txt"))
	if err != nil || string(body) != "not a worktree" {
		t.Errorf("occupant must be untouched, body=%q err=%v", body, err)
	}
	// The manifest entry must still be present -- nothing was restored.
	manifest := Load()
	if len(manifest) != 1 {
		t.Fatalf("manifest = %+v, want the entry to survive a refused restore", manifest)
	}
}

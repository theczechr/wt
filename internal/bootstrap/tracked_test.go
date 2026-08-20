package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/config"
)

// gitRepoWithFile builds a real git repo in dir containing one committed
// file, and returns the repo path. It exists because the distinction under
// test -- tracked versus hand-placed -- only exists inside a real repo; a
// bare temp directory cannot express it.
func gitRepoWithFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", dir)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "-C", dir, "add", name)
	runGit(t, dir, "-C", dir, "commit", "-qm", "add "+name)
	return dir
}

// TestLinkEnvSkipsTrackedFileWithoutError pins the fix for the bug that
// produced unusable worktrees: .env.test is committed to the repo, so git
// checks it out into every new worktree, and LinkEnv found a real file at
// dst on a completely routine bootstrap. The refusal to overwrite is
// correct and must stay -- a symlink over a tracked file leaves the
// worktree permanently dirty with a type change -- but it is not an error,
// and reporting it as one aborted bootstrap before the submodule stage.
//
// The file must survive untouched and unconverted, and no error may be
// reported for it.
func TestLinkEnvSkipsTrackedFileWithoutError(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	target := gitRepoWithFile(t, filepath.Join(root, "target"), ".env.test", "tracked body")

	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, ".env.test"), []byte("primary body"), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := LinkEnv(primary, target, config.Repo{Env: []string{".env.test"}})
	if len(errs) != 0 {
		t.Fatalf("tracked file reported as an error: %v", errs)
	}

	dst := filepath.Join(target, ".env.test")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("tracked file disappeared: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("tracked file was replaced by a symlink: the worktree is now dirty with a type change")
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "tracked body" {
		t.Errorf("tracked file content changed: got %q, want %q", body, "tracked body")
	}
}

// TestLinkEnvStillRefusesUntrackedRegularFile is the other half, and the
// more important one: the tracked-file exemption must not become a general
// licence to overwrite. A real file nobody committed -- a hand-copied .env
// in one of the user's sibling worktrees, holding credentials that exist
// nowhere else -- is still refused, still reported, and still left exactly
// as it was.
func TestLinkEnvStillRefusesUntrackedRegularFile(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	target := gitRepoWithFile(t, filepath.Join(root, "target"), "README", "x")

	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, ".env"), []byte("primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Untracked: written directly, never added to the index.
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("irreplaceable"), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := LinkEnv(primary, target, config.Repo{Env: []string{".env"}})
	if len(errs) != 1 {
		t.Fatalf("expected exactly one refusal, got %v", errs)
	}
	if !strings.Contains(errs[0].Error(), "refusing to delete") {
		t.Errorf("unexpected error text: %v", errs[0])
	}

	body, err := os.ReadFile(filepath.Join(target, ".env"))
	if err != nil {
		t.Fatalf("untracked file destroyed: %v", err)
	}
	if string(body) != "irreplaceable" {
		t.Errorf("untracked file overwritten: got %q", body)
	}
}

// TestLinkEnvTreatsUnknownTrackednessAsUntracked pins the polarity of the
// git probe against the package invariant: only a positive "yes, tracked"
// answer may downgrade an entry to a silent skip. Here target is not a git
// repository at all, so `git ls-files` cannot answer -- and unknown must
// route back to the refusal that never deletes anything, not to the quiet
// branch.
func TestLinkEnvTreatsUnknownTrackednessAsUntracked(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	target := filepath.Join(root, "target") // deliberately not a git repo

	for _, d := range []string{primary, target} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(primary, ".env"), []byte("primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := LinkEnv(primary, target, config.Repo{Env: []string{".env"}})
	if len(errs) != 1 {
		t.Fatalf("unknown trackedness was treated as safe: %v", errs)
	}
	body, _ := os.ReadFile(filepath.Join(target, ".env"))
	if string(body) != "keep me" {
		t.Errorf("file not preserved: got %q", body)
	}
}

// TestRunContinuesPastEnvFailure is the defect that actually cost the user a
// working worktree. Bootstrap's stages are independent, but Run returned at
// the first env error, so a refused env entry silently skipped submodule
// initialisation -- leaving a checkout with an empty submodule directory
// that could not build. Both stages must run, and both failures must be
// reported.
func TestRunContinuesPastEnvFailure(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	target := gitRepoWithFile(t, filepath.Join(root, "target"), "README", "x")

	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, ".env"), []byte("primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("hand-placed"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), primary, target, config.Repo{
		Env:        []string{".env"},
		Submodules: []string{"no-such-submodule"},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "refusing to delete") {
		t.Errorf("env failure not reported: %v", err)
	}
	if !strings.Contains(msg, "no-such-submodule") {
		t.Errorf("submodule stage was skipped after the env failure -- this is the bug: %v", err)
	}
}

// TestRunSkipsPostCreateAfterAFailureAndSaysSo checks the one stage that is
// deliberately gated. post_create commands assume env files and submodules
// are already in place, so running them over a half-prepared worktree only
// produces a second, more confusing failure. It is skipped -- and the skip
// is reported, so a partial bootstrap is never mistaken for a complete one.
func TestRunSkipsPostCreateAfterAFailureAndSaysSo(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	target := gitRepoWithFile(t, filepath.Join(root, "target"), "README", "x")
	sentinel := filepath.Join(root, "post-create-ran")

	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, ".env"), []byte("primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("hand-placed"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), primary, target, config.Repo{
		Env:        []string{".env"},
		PostCreate: []string{"touch " + sentinel},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "post_create skipped") {
		t.Errorf("skip not reported to the caller: %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Error("post_create ran over a half-prepared worktree")
	}
}

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/theczechr/wt/internal/config"
)

// TestCreateRejectsEmptyPrimary guards against Defect G: primaryPath in
// cmd/wt/main.go can return "" when a configured repo isn't found under any
// scan root, and an unguarded empty primary makes Create operate relative to
// the caller's cwd instead of failing — "git -C <empty>" is a documented no-op, so
// git runs against whatever repo the process happens to be standing in.
//
// This reproduces that scenario for real: it puts a valid git repo at the
// process cwd (so a missing guard would let git silently succeed there) and
// asserts Create refuses to touch it when primary is "".
func TestCreateRejectsEmptyPrimary(t *testing.T) {
	cwdRepo := t.TempDir()
	runGit(t, cwdRepo, "init", "-q")
	runGit(t, cwdRepo, "commit", "--allow-empty", "-q", "-m", "init")
	t.Chdir(cwdRepo)

	target, err := Create(context.Background(), "", "somerepo", "somebranch", "", config.Repo{})
	if err == nil {
		t.Fatal("expected an error when primary is empty")
	}
	if target != "" {
		t.Errorf("expected no target path on error, got %q", target)
	}

	entries, readErr := os.ReadDir(cwdRepo)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			t.Errorf("Create must not create anything in the cwd repo, found %q", e.Name())
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wt-test", "GIT_AUTHOR_EMAIL=wt-test@example.com",
		"GIT_COMMITTER_NAME=wt-test", "GIT_COMMITTER_EMAIL=wt-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestCreateRejectsInvalidNames asserts Create refuses a name argument that
// would escape the flat-sibling guarantee documented on Create: a path
// separator would nest instead of producing a flat sibling, ".." components
// escape primary's parent directory entirely, and an empty-after-trim name
// is meaningless as an explicit override.
//
// This uses a real, valid primary git repo (not a bogus path) specifically
// so the assertion exercises name validation, not some unrelated failure --
// `git -C <bogus primary>` would itself fail and return an error for the
// wrong reason, masking a missing validation. With a real repo, `git
// worktree add` auto-creates intermediate directories and would happily
// succeed for every one of these names pre-fix, actually nesting or
// escaping as warned above.
func TestCreateRejectsInvalidNames(t *testing.T) {
	// root stays the sandbox for everything this test touches, including the
	// "escapes via .." case below -- that name must resolve to somewhere
	// still under root, never outside t.TempDir().
	root := t.TempDir()
	parent := filepath.Join(root, "checkouts")
	primary := filepath.Join(parent, "backend")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, primary, "init", "-q")
	runGit(t, primary, "commit", "--allow-empty", "-q", "-m", "init")

	cases := []struct {
		label string
		name  string
	}{
		{"nested path separator", "sub/dir"},
		{"escapes via ..", "../escaped-outside-parent"},
		{"backslash", `back\slash`},
		{"whitespace only", "   "},
	}
	for i, c := range cases {
		branch := fmt.Sprintf("branch-%d", i)
		target, err := Create(context.Background(), primary, "backend", branch, c.name, config.Repo{})
		if err == nil {
			t.Errorf("%s (%q): expected an error, got target %q", c.label, c.name, target)
		}
		if target != "" {
			t.Errorf("%s (%q): expected no target path on error, got %q", c.label, c.name, target)
		}
	}

	// Nothing beyond the primary checkout itself may exist under parent:
	// every case above must have been refused before git ever ran.
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "backend" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("parent directory must contain only %q, got %v", "backend", names)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped-outside-parent")); !os.IsNotExist(err) {
		t.Error("the \"..\" name must not have created anything outside the checkouts directory")
	}
}

// TestCreateAcceptsValidExplicitName is the sanity check alongside the
// rejection test above: a plain, unambiguous name must still work.
func TestCreateAcceptsValidExplicitName(t *testing.T) {
	parent := t.TempDir()
	primary := filepath.Join(parent, "backend")
	if err := os.Mkdir(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, primary, "init", "-q")
	runGit(t, primary, "commit", "--allow-empty", "-q", "-m", "init")

	target, err := Create(context.Background(), primary, "backend", "some-branch", "backend-my-custom-name", config.Repo{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(parent, "backend-my-custom-name")
	if target != want {
		t.Errorf("target = %q, want %q", target, want)
	}
}

// TestCreateReturnsEmptyTargetWhenWorktreeAddFails pins the "" half of
// Create's error-path contract: when `git worktree add` itself fails, nothing
// was created on disk, so the returned path must be empty -- a non-empty
// path here would send a caller after a directory that doesn't exist.
func TestCreateReturnsEmptyTargetWhenWorktreeAddFails(t *testing.T) {
	parent := t.TempDir()
	primary := filepath.Join(parent, "backend")
	if err := os.Mkdir(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, primary, "init", "-q")
	runGit(t, primary, "commit", "--allow-empty", "-q", "-m", "init")

	// "bad..branch" is rejected by git's check-ref-format (a ref component
	// may not contain ".."), so `worktree add -b` fails before anything is
	// created on disk.
	target, err := Create(context.Background(), primary, "backend", "bad..branch", "", config.Repo{})
	if err == nil {
		t.Fatal("expected an error for an invalid branch name")
	}
	if target != "" {
		t.Errorf("target = %q, want \"\": a failed worktree add must report no path, since nothing was created", target)
	}
}

// TestCreateReturnsTargetWhenBootstrapFails pins the non-empty half of the
// same contract: when the worktree is created but the bootstrap step (here a
// failing post_create hook) fails afterward, a real worktree exists and the
// caller should be told where to go look at it.
func TestCreateReturnsTargetWhenBootstrapFails(t *testing.T) {
	parent := t.TempDir()
	primary := filepath.Join(parent, "backend")
	if err := os.Mkdir(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, primary, "init", "-q")
	runGit(t, primary, "commit", "--allow-empty", "-q", "-m", "init")

	target, err := Create(context.Background(), primary, "backend", "some-branch", "",
		config.Repo{PostCreate: []string{"exit 1"}})
	if err == nil {
		t.Fatal("expected an error when post_create fails")
	}
	want := filepath.Join(parent, "backend-some-branch")
	if target != want {
		t.Errorf("target = %q, want %q: a failed bootstrap leaves a real worktree behind and the caller must be told where", target, want)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("worktree must actually exist at %q: %v", target, statErr)
	}
}

func TestSlugStripsTypePrefixes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fix/rate-calc-nan-guard", "rate-calc-nan-guard"},
		{"feature/refund-payment-queue", "refund-payment-queue"},
		{"hotfix/export-request-suspend-status", "export-request-suspend-status"},
		{"perf/drop-dead-account-balance-1h-indexes", "drop-dead-account-balance-1h-indexes"},
		{"ci/gate-admin-api-tests", "gate-admin-api-tests"},
		{"develop", "develop"},
		{"sprout/fix/admin-report-export", "sprout-fix-admin-report-export"},
	}
	for _, c := range cases {
		if got := Slug(c.in); got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

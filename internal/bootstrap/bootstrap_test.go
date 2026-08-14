package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/config"
)

func TestLinkEnvSymlinksListedFilesAndCopiesOverrides(t *testing.T) {
	primary := t.TempDir()
	target := t.TempDir()
	for _, n := range []string{".env", ".env.prod", ".env.test"} {
		if err := os.WriteFile(filepath.Join(primary, n), []byte("K=v"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := config.Repo{
		Env:  []string{".env", ".env.prod", ".env.test"},
		Copy: []string{".env.test"},
	}
	if errs := LinkEnv(primary, target, r); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	fi, err := os.Lstat(filepath.Join(target, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error(".env must be a symlink")
	}

	fi, err = os.Lstat(filepath.Join(target, ".env.test"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error(".env.test is in Copy and must be a real file")
	}
	body, _ := os.ReadFile(filepath.Join(target, ".env.test"))
	if string(body) != "K=v" {
		t.Errorf("copied content = %q", body)
	}
}

func TestLinkEnvSkipsMissingSourcesWithoutFailing(t *testing.T) {
	primary := t.TempDir()
	target := t.TempDir()
	r := config.Repo{Env: []string{".env.absent"}}
	if errs := LinkEnv(primary, target, r); len(errs) != 0 {
		t.Errorf("absent source must be skipped, got %v", errs)
	}
	if _, err := os.Lstat(filepath.Join(target, ".env.absent")); err == nil {
		t.Error("must not create a dangling link")
	}
}

func TestLinkEnvRejectsPathTraversalAndPreservesEscapeTargets(t *testing.T) {
	root := t.TempDir()
	// target sits two levels below root, so "../escape.env" and
	// "../../escape.env" resolve to two different real, existing files.
	target := filepath.Join(root, "a", "target")
	primary := filepath.Join(root, "primary")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}

	oneUp := filepath.Join(root, "a", "escape.env") // hit by "../escape.env" and "sub/../../escape.env"
	twoUp := filepath.Join(root, "escape.env")      // hit by "../../escape.env"
	if err := os.WriteFile(oneUp, []byte("SECRET-ONE-UP"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(twoUp, []byte("SECRET-TWO-UP"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []string{"../escape.env", "../../escape.env", "sub/../../escape.env"}
	for _, name := range cases {
		r := config.Repo{Env: []string{name}}
		errs := LinkEnv(primary, target, r)
		if len(errs) != 1 {
			t.Errorf("%s: expected exactly one error, got %v", name, errs)
		}
	}

	oneUpBody, err := os.ReadFile(oneUp)
	if err != nil {
		t.Fatal(err)
	}
	if string(oneUpBody) != "SECRET-ONE-UP" {
		t.Errorf("file at escape target (one level up) was modified: %q", oneUpBody)
	}
	twoUpBody, err := os.ReadFile(twoUp)
	if err != nil {
		t.Fatal(err)
	}
	if string(twoUpBody) != "SECRET-TWO-UP" {
		t.Errorf("file at escape target (two levels up) was modified: %q", twoUpBody)
	}
}

func TestLinkEnvAcceptsLegitimateNestedEntry(t *testing.T) {
	primary := t.TempDir()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(primary, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, "config", ".env.local"), []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := config.Repo{Env: []string{"config/.env.local"}}
	if errs := LinkEnv(primary, target, r); len(errs) != 0 {
		t.Fatalf("legitimate nested entry must be accepted, got %v", errs)
	}
	if _, err := os.Lstat(filepath.Join(target, "config", ".env.local")); err != nil {
		t.Errorf("expected symlink to be created: %v", err)
	}
}

// TestSubmodulesReturnsErrorWhenStatusCheckFails asserts that Submodules
// propagates a failure of the final `git submodule status` verification
// instead of swallowing it and reporting success. It fakes `git` on PATH so
// `submodule update` succeeds (as it would for an already-initialised
// submodule) while `submodule status` fails, isolating the status-check
// regression specifically.
func TestSubmodulesReturnsErrorWhenStatusCheckFails(t *testing.T) {
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
for a in "$@"; do
  if [ "$a" = "status" ]; then
    echo "fatal: fake status failure" >&2
    exit 1
  fi
done
exit 0
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(fakeGit)+string(os.PathListSeparator)+os.Getenv("PATH"))

	target := t.TempDir()
	r := config.Repo{Submodules: []string{"vendor/corelib"}}
	err := Submodules(context.Background(), target, r)
	if err == nil {
		t.Fatal("expected a non-nil error when the status check fails")
	}
	if !strings.Contains(err.Error(), "submodule status") {
		t.Errorf("error must mention the status failure, got %q", err)
	}
}

// TestLinkEnvSamePathLeavesEveryFileIntact reproduces the critical data-loss
// bug: `wt bootstrap <primary-checkout>` called with target == primary. src
// and dst are then the same path for every entry -- the existing containment
// guard passes trivially (Rel of a directory against itself is clean),
// os.Stat(src) succeeds, and the pre-fix code's unconditional os.Remove(dst)
// deletes the real file before symlinking it to itself. This asserts every
// configured file survives byte-identical and is never turned into a
// symlink.
func TestLinkEnvSamePathLeavesEveryFileIntact(t *testing.T) {
	primary := t.TempDir()
	names := []string{".env", ".env.dev", ".env.prod", ".env.test"}
	want := map[string]string{}
	for _, n := range names {
		body := "SECRET_" + n + "=do-not-delete-me"
		want[n] = body
		if err := os.WriteFile(filepath.Join(primary, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := config.Repo{Env: names, Copy: []string{".env.test"}}

	// target == primary: the exact shape of "wt bootstrap <primary-checkout>".
	_ = LinkEnv(primary, primary, r)

	for _, n := range names {
		fi, err := os.Lstat(filepath.Join(primary, n))
		if err != nil {
			t.Fatalf("%s: file must still exist: %v", n, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s: must NOT have been turned into a symlink", n)
		}
		body, err := os.ReadFile(filepath.Join(primary, n))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if string(body) != want[n] {
			t.Errorf("%s: content = %q, want %q (byte-identical)", n, body, want[n])
		}
	}
}

// TestLinkEnvNeverDeletesARegularFileAtDestination covers the second,
// independent critical this fix addresses: the user's hand-made sibling
// worktrees have real, possibly-diverged env files that were manually copied
// in, not symlinked by wt. Running LinkEnv against one of those must not
// delete the real file at dst just because a symlink entry is configured for
// that name.
func TestLinkEnvNeverDeletesARegularFileAtDestination(t *testing.T) {
	primary := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, ".env.dev"), []byte("PRIMARY-VALUE"), 0o600); err != nil {
		t.Fatal(err)
	}
	const handCopiedBody = "HAND-COPIED-DIVERGED-VALUE"
	if err := os.WriteFile(filepath.Join(target, ".env.dev"), []byte(handCopiedBody), 0o600); err != nil {
		t.Fatal(err)
	}

	r := config.Repo{Env: []string{".env.dev"}}
	errs := LinkEnv(primary, target, r)
	if len(errs) == 0 {
		t.Fatal("expected an error reporting the pre-existing real file, got none")
	}

	fi, err := os.Lstat(filepath.Join(target, ".env.dev"))
	if err != nil {
		t.Fatalf("real file must still exist: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("real file must NOT have been replaced with a symlink")
	}
	body, err := os.ReadFile(filepath.Join(target, ".env.dev"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != handCopiedBody {
		t.Errorf("content = %q, want %q (the hand-copied file must survive untouched)", body, handCopiedBody)
	}
}

// TestLinkEnvReplacesExistingSymlinkAtDestination asserts the idempotent
// re-bootstrap case still works after the fix: an existing SYMLINK at dst
// (from a previous bootstrap run) is safe to remove and recreate, unlike a
// regular file.
func TestLinkEnvReplacesExistingSymlinkAtDestination(t *testing.T) {
	primary := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, ".env"), []byte("V1"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := config.Repo{Env: []string{".env"}}
	if errs := LinkEnv(primary, target, r); len(errs) != 0 {
		t.Fatalf("first run: unexpected errors: %v", errs)
	}

	// Rewrite the source; a second run must still resolve to the new content
	// through a freshly-created symlink, not choke on the pre-existing link.
	if err := os.WriteFile(filepath.Join(primary, ".env"), []byte("V2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if errs := LinkEnv(primary, target, r); len(errs) != 0 {
		t.Fatalf("second run: unexpected errors: %v", errs)
	}

	fi, err := os.Lstat(filepath.Join(target, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dst must still be a symlink after re-linking")
	}
	body, err := os.ReadFile(filepath.Join(target, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "V2" {
		t.Errorf("content via symlink = %q, want %q", body, "V2")
	}
}

// TestLinkEnvCopyEntryOverwritesRegularFileInPlace asserts that for a
// Copy-listed entry, a pre-existing regular file at dst is still overwritten
// -- that is the intended, documented behaviour, and is the one case where a
// real file at dst is expected to change.
func TestLinkEnvCopyEntryOverwritesRegularFileInPlace(t *testing.T) {
	primary := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, ".env.test"), []byte("NEW-VALUE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".env.test"), []byte("OLD-VALUE"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := config.Repo{Env: []string{".env.test"}, Copy: []string{".env.test"}}
	if errs := LinkEnv(primary, target, r); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	fi, err := os.Lstat(filepath.Join(target, ".env.test"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("Copy-listed entry must remain a real file, not a symlink")
	}
	body, err := os.ReadFile(filepath.Join(target, ".env.test"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "NEW-VALUE" {
		t.Errorf("content = %q, want %q", body, "NEW-VALUE")
	}
}

// TestContainedResistsSymlinkedParentDirectory covers Fix 2: a nested entry
// whose parent directory is a symlink (a git checkout can legitimately
// contain a committed symlink) must not be treated as contained just because
// the lexical join looks clean, since os.Remove would follow the symlink and
// delete through it, outside target.
func TestContainedResistsSymlinkedParentDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// target/config is a symlink pointing outside target.
	if err := os.Symlink(outside, filepath.Join(target, "config")); err != nil {
		t.Fatal(err)
	}

	resolved := filepath.Join(target, "config", ".env.local")
	if contained(target, resolved) {
		t.Error("a nested entry through a symlinked parent must not be reported as contained")
	}
}

// TestContainedAllowsFreshNestedParent asserts the symlink-aware check in
// Fix 2 does not regress the ordinary case: a nested entry whose parent
// directory does not exist yet (a fresh worktree, nothing checked out under
// it yet) must still be accepted, falling back to the lexical check.
func TestContainedAllowsFreshNestedParent(t *testing.T) {
	target := t.TempDir()
	resolved := filepath.Join(target, "config", ".env.local")
	if !contained(target, resolved) {
		t.Error("a nested entry under a not-yet-existing parent must be accepted")
	}
}

func TestLinkEnvIsIdempotent(t *testing.T) {
	primary := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, ".env"), []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := config.Repo{Env: []string{".env"}}
	LinkEnv(primary, target, r)
	if errs := LinkEnv(primary, target, r); len(errs) != 0 {
		t.Errorf("second run must succeed, got %v", errs)
	}
}

// TestSamePathAbsolutisesRelativeArgument covers Fix 1: primary always
// arrives absolute (from `git rev-parse --path-format=absolute`), but target
// is the raw os.Args[2] and can be relative -- e.g. `wt bootstrap .` from
// inside the primary checkout, or `wt bootstrap server` from its parent.
// Pre-fix, samePath compared an absolute string against a relative one
// without ever calling filepath.Abs, so it returned false even when both
// named the same directory.
func TestSamePathAbsolutisesRelativeArgument(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	t.Chdir(resolved)
	if !SamePath(resolved, ".") {
		t.Error(`SamePath(resolved, ".") = false, want true: cwd IS resolved`)
	}

	parent := filepath.Dir(resolved)
	base := filepath.Base(resolved)
	t.Chdir(parent)
	if !SamePath(resolved, base) {
		t.Errorf("SamePath(resolved, %q) = false, want true: %q is a relative spelling of resolved", base, base)
	}
}

// TestLinkEnvSelfLinkDetectedThroughRelativeTarget is the test that matters
// most for Fix 1. It reproduces the silent-data-loss case precisely:
// primary is passed absolute, target is the relative spelling of that exact
// same directory (as os.Args[2] would arrive from `wt bootstrap primary` run
// from primary's parent, or `wt bootstrap .` from inside primary), and one
// of the configured env entries is ITSELF a symlink pointing at a real
// external secrets store (e.g. .env.prod -> a shared secrets file) rather
// than a plain regular file.
//
// Pre-fix, samePath(primary, target) returns false for this absolute-vs-
// relative pair (see TestSamePathAbsolutisesRelativeArgument), so selfLink
// is wrongly computed as false and layer (a) never fires. LinkEnv then
// treats the entry as an ordinary idempotent re-link: it finds a symlink at
// dst (the same file the relative dst string resolves to), removes it --
// destroying the only pointer to the real secrets file -- and replaces it
// with a symlink pointing at itself. It reports no error, and the caller
// prints "bootstrapped ...".
//
// This must fail against the pre-fix samePath.
func TestLinkEnvSelfLinkDetectedThroughRelativeTarget(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	primaryAbs, err := filepath.EvalSymlinks(primary)
	if err != nil {
		t.Fatal(err)
	}

	secretsDir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(secretsDir, "real.env.prod")
	if err := os.WriteFile(secretPath, []byte("REAL-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	envLink := filepath.Join(primaryAbs, ".env.prod")
	if err := os.Symlink(secretPath, envLink); err != nil {
		t.Fatal(err)
	}

	// `wt bootstrap primary`, run from primary's own parent directory: target
	// is the raw, relative os.Args[2] spelling of primaryAbs.
	t.Chdir(root)
	relativeTarget := "primary"

	r := config.Repo{Env: []string{".env.prod"}}
	_ = LinkEnv(primaryAbs, relativeTarget, r)

	fi, err := os.Lstat(envLink)
	if err != nil {
		t.Fatalf(".env.prod must still exist: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal(".env.prod must still be a symlink")
	}
	linkTarget, err := os.Readlink(envLink)
	if err != nil {
		t.Fatal(err)
	}
	if linkTarget != secretPath {
		t.Errorf("symlink target = %q, want %q: must still point at the real secrets store, not a self-reference", linkTarget, secretPath)
	}
	body, err := os.ReadFile(envLink)
	if err != nil {
		t.Fatalf("must still be readable through the link: %v", err)
	}
	if string(body) != "REAL-SECRET" {
		t.Errorf("content via symlink = %q, want %q", body, "REAL-SECRET")
	}
}

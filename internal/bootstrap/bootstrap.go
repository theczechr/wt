// Package bootstrap prepares a freshly created worktree for use.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/theczechr/wt/internal/config"
)

func inCopyList(name string, r config.Repo) bool {
	for _, c := range r.Copy {
		if c == name {
			return true
		}
	}
	return false
}

// contained reports whether resolved lies within base, guarding against both
// a lexical escape (a config entry containing "..") and a symlink escape (a
// nested entry, e.g. "config/.env.local", whose parent directory is a
// committed symlink pointing outside base -- os.Remove would otherwise walk
// through it at removal time even though the join looks contained).
//
// The lexical check runs first and unconditionally. The symlink-aware check
// only tightens that verdict when resolved's parent directory actually
// exists on disk; a not-yet-existing parent is normal for a fresh worktree
// and is not treated as a failure, so it falls back to trusting the lexical
// check rather than erroring.
func contained(base, resolved string) bool {
	rel, err := filepath.Rel(base, resolved)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}

	parentReal, err := filepath.EvalSymlinks(filepath.Dir(resolved))
	if err != nil {
		return true // parent doesn't exist yet; trust the lexical check above
	}
	baseReal, err := filepath.EvalSymlinks(base)
	if err != nil {
		baseReal = base
	}
	resolvedReal := filepath.Join(parentReal, filepath.Base(resolved))

	rel, err = filepath.Rel(baseReal, resolvedReal)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// samePath reports whether a and b refer to the same file on disk. Both
// arguments are absolutised first: primary always arrives absolute (from
// `git rev-parse --path-format=absolute`), but a caller-supplied target
// (e.g. os.Args[2], or a relative spelling like "." or "backend") does not,
// and filepath.EvalSymlinks(".") happily resolves to "." rather than an
// error -- so comparing an absolute path against an un-absolutised relative
// one would silently return false even when they name the same directory.
// After absolutising, it resolves symlinks when both paths exist; when
// either does not (or the resolution fails), it falls back to comparing
// cleaned paths, which is exactly enough to catch the case that matters
// most: `wt bootstrap <primary-checkout>` called with the primary's own
// path, where src and dst are computed from the same directory and are
// lexically identical without needing anything on disk to resolve.
func samePath(a, b string) bool {
	a, b = absClean(a), absClean(b)
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return ra == rb
	}
	return a == b
}

// absClean absolutises p (falling back to p unchanged if filepath.Abs
// fails, which only happens if os.Getwd fails) and cleans it. It is the raw,
// symlink-blind half of samePath's comparison, split out so callers that
// need to compare two paths' identity WITHOUT following an existing symlink
// at either end -- see the LinkEnv re-link guard below -- can do so without
// duplicating the absolutisation logic.
func absClean(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

// SamePath reports whether primary and target are the same directory, so
// callers can refuse to bootstrap a repo's own primary checkout: source and
// destination would be identical, and linking a file to itself can only
// destroy it. Exported for cmd/wt/main.go's pre-flight guard.
func SamePath(primary, target string) bool {
	return samePath(primary, target)
}

// LinkEnv symlinks each configured env file from the primary checkout into the
// target worktree, so a rotated credential propagates instead of going stale.
// Files listed in Copy are copied instead, for branches that must diverge.
// A missing source is skipped, never linked as a dangling symlink.
//
// Every name comes verbatim from user-edited TOML with no sanitisation at
// load time, and filepath.Join does not sandbox "..". A config entry such as
// "../.env" is a plausible typo, not just an attack, and would otherwise
// resolve outside target/primary, so every entry is checked for containment
// before any filesystem mutation.
//
// Two further guards protect real files from ever being silently destroyed:
//
//  1. If primary and target are the same directory (bootstrapping a repo's
//     own primary checkout, by typo or by a hook firing on the wrong
//     payload) src and dst are literally the same path. Nothing sensible can
//     be done when a file would be linked to itself, so the entry is skipped
//     outright rather than removed and relinked.
//  2. dst is never removed just because something is about to replace it.
//     A missing dst is created as before. An existing symlink at dst is safe
//     to remove and recreate -- that's the idempotent re-bootstrap case. But
//     an existing REGULAR file at dst is never deleted: for a plain (non-Copy)
//     entry that would destroy a real file (e.g. a hand-copied .env in one of
//     the user's hand-made sibling worktrees) to make room for a symlink, so
//     the entry is left alone and reported as an error instead. Copy-listed
//     entries are the one case where overwriting a regular file in place is
//     the intended behaviour, and os.WriteFile's truncate-and-write does that
//     without ever calling os.Remove.
func LinkEnv(primary, target string, r config.Repo) []error {
	var errs []error
	// Computed once against the two directories themselves, not per-entry
	// against src/dst: after a real link is created, dst legitimately
	// resolves (through its own symlink) to the same real path as src, and
	// that must NOT be confused with primary and target being the same
	// directory. Comparing the directories up front sidesteps that entirely.
	selfLink := samePath(primary, target)
	for _, name := range r.Env {
		src := filepath.Join(primary, name)
		dst := filepath.Join(target, name)

		if !contained(target, dst) || !contained(primary, src) {
			errs = append(errs, fmt.Errorf("%s: env entry escapes its worktree root, refusing to link", name))
			continue
		}

		if selfLink {
			errs = append(errs, fmt.Errorf("%s: primary and target are the same directory; refusing to link a file to itself", name))
			continue
		}

		if _, err := os.Stat(src); err != nil {
			continue // nothing to link
		}

		isCopy := inCopyList(name, r)

		dstInfo, statErr := os.Lstat(dst)
		switch {
		case statErr == nil && dstInfo.Mode()&os.ModeSymlink != 0:
			// Idempotent re-link: safe to remove and recreate -- but only if
			// src and dst don't literally name the same path. That should
			// already be impossible (selfLink is checked above), but this is
			// belt-and-braces: verify again here, directly against this
			// entry's own src/dst, so a bug in the outer guard can't reach
			// os.Remove.
			//
			// This deliberately does NOT reuse samePath, which resolves
			// symlinks: in the ordinary, expected state at this point dst IS
			// already a valid symlink whose target is src, so
			// EvalSymlinks(dst) and EvalSymlinks(src) always agree -- that
			// check would fire on every routine re-link, not just the
			// dangerous case. What actually distinguishes "primary == target,
			// about to self-link" from "a normal re-link in a distinct
			// worktree" is whether src and dst are the same path BEFORE
			// either is dereferenced, so this compares them raw (absolutised
			// and cleaned, symlinks untouched).
			if rawSrc, rawDst := absClean(src), absClean(dst); rawSrc == rawDst {
				errs = append(errs, fmt.Errorf("%s: refusing to remove %s: src and dst are the same path", name, dst))
				continue
			}
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
		case statErr == nil && !isCopy:
			// A real file sits at dst and this is a symlink entry: never
			// destroy it. Report and move on.
			errs = append(errs, fmt.Errorf("%s: a real file already exists at %s; refusing to delete it (bootstrap does not overwrite real files)", name, dst))
			continue
		case statErr != nil && !os.IsNotExist(statErr):
			errs = append(errs, fmt.Errorf("%s: %w", name, statErr))
			continue
		}
		// Remaining cases: dst absent, or dst is a regular file in a
		// Copy-listed entry -- both fall through to creation below, which
		// overwrites in place without a preceding Remove.

		if isCopy {
			body, err := os.ReadFile(src)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			if err := os.WriteFile(dst, body, 0o600); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
			continue
		}
		if err := os.Symlink(src, dst); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errs
}

// Submodules initialises each configured submodule at the SHA the branch
// records, then asserts it is clean. An unpinned or dirty submodule is a known
// cause of silent typecheck failure, so it is reported rather than ignored.
func Submodules(ctx context.Context, target string, r config.Repo) error {
	for _, sm := range r.Submodules {
		cmd := exec.CommandContext(ctx, "git", "-C", target,
			"submodule", "update", "--init", "--recursive", sm)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("submodule %s: %w: %s", sm, err, out)
		}
	}
	if len(r.Submodules) == 0 {
		return nil
	}
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", target,
		"submodule", "status").Output()
	if err != nil {
		return fmt.Errorf("submodule status: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// A leading '+' means the checked-out SHA differs from the pin.
		if strings.HasPrefix(line, "+") {
			return fmt.Errorf("submodule not at pinned SHA: %s", strings.TrimSpace(line))
		}
	}
	return nil
}

// Run performs the whole bootstrap: env files, submodules, post-create hooks.
func Run(ctx context.Context, primary, target string, r config.Repo) error {
	if errs := LinkEnv(primary, target, r); len(errs) > 0 {
		return fmt.Errorf("env: %v", errs)
	}
	if err := Submodules(ctx, target, r); err != nil {
		return err
	}
	for _, c := range r.PostCreate {
		cmd := exec.CommandContext(ctx, "sh", "-c", c)
		cmd.Dir = target
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("post_create %q: %w: %s", c, err, out)
		}
	}
	return nil
}

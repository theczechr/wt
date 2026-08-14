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

var typePrefixes = []string{
	"feature/", "feat/", "fix/", "hotfix/", "perf/",
	"ci/", "test/", "docs/", "chore/", "refactor/",
}

// Slug turns a branch name into a directory suffix: the leading type prefix is
// stripped and remaining slashes become dashes.
func Slug(branch string) string {
	s := branch
	for _, p := range typePrefixes {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
			break
		}
	}
	return strings.ReplaceAll(s, "/", "-")
}

// Create adds a flat sibling worktree next to the primary checkout and
// bootstraps it. An existing path is an error: wt never invents a variant name.
//
// primary must be a non-empty, resolved path to the repo's primary checkout.
// An empty primary is refused rather than tolerated: filepath.Dir("") is ".",
// and "git -C <empty>" is a documented no-op that makes git operate on the
// caller's cwd instead of failing — so a blank primary would otherwise create
// a worktree inside whatever repository the process happens to be standing
// in, silently. See the "refuse rather than invent" design principle.
func Create(ctx context.Context, primary, repoName, branch, name string, r config.Repo) (string, error) {
	if primary == "" {
		return "", fmt.Errorf("no primary checkout found for repo %q; refusing to create a worktree relative to the current directory", repoName)
	}
	// An empty name means "not supplied": generate one below. Anything the
	// caller DID supply is validated -- filepath.Join does not sandbox "..",
	// and a name containing a path separator would nest instead of
	// producing the flat sibling this function guarantees, or (with enough
	// "..") escape the primary's parent directory entirely.
	if name != "" {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			return "", fmt.Errorf("invalid worktree name %q: must be non-empty and must not contain a path separator or \"..\"", name)
		}
	}
	if name == "" {
		name = repoName + "-" + Slug(branch)
	}
	target := filepath.Join(filepath.Dir(primary), name)
	if err := CreateAt(ctx, primary, target, branch, "", r); err != nil {
		// Report the path only when it actually exists: a failed `worktree
		// add` leaves nothing behind, while a failed bootstrap leaves a real
		// worktree the caller should be told to go and look at. Callers
		// distinguish those by whether the path is empty, so returning one
		// that isn't there would send them after a directory that was never
		// created.
		if _, statErr := os.Stat(target); statErr != nil {
			return "", err
		}
		return target, err
	}
	return target, nil
}

// CreateAt adds a worktree at an explicit path and bootstraps it.
//
// startPoint is the commit-ish the branch is created from when it does not
// already exist locally -- typically "origin/<branch>" for a branch that
// exists only upstream. Passing "" reproduces git's default of branching from
// the primary's current HEAD, which is right for a genuinely new branch and
// wrong for every other case: for a remote-only branch it would silently
// produce an EMPTY branch rather than the code the caller asked for.
func CreateAt(ctx context.Context, primary, target, branch, startPoint string, r config.Repo) error {
	if primary == "" {
		return fmt.Errorf("no primary checkout given; refusing to create a worktree relative to the current directory")
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	args := []string{"-C", primary, "worktree", "add", target}
	switch {
	case branchExists(ctx, primary, branch):
		args = append(args, branch)
	case startPoint != "":
		args = append(args, "-b", branch, startPoint)
	default:
		args = append(args, "-b", branch)
	}
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("worktree add: %w: %s", err, out)
	}
	if err := Run(ctx, primary, target, r); err != nil {
		return fmt.Errorf("created %s but bootstrap failed: %w", target, err)
	}
	return nil
}

func branchExists(ctx context.Context, primary, branch string) bool {
	err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary,
		"show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()
	return err == nil
}

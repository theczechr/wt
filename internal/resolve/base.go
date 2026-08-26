package resolve

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/theczechr/wt/internal/config"
)

// Base resolves the start point a NEW branch should be cut from, and fetches
// it first.
//
// The fallback order matters more than it looks. Creating a branch off the
// primary checkout's current HEAD -- git's own default for
// `git worktree add -b`, and what `wt new` did -- is almost never what
// someone means by "start new work". A primary checkout sits on whatever
// branch was last worked on there; on this machine that is a hotfix, so
// every "new" worktree would silently be cut from an unrelated fix rather
// than from a mainline. On a repo where migration filenames must sort above
// everything already merged, that is not a cosmetic problem.
//
// So:
//
//  1. The repo's configured `base`, when set. Explicit beats inferred.
//  2. The remote's own default branch, read from refs/remotes/<remote>/HEAD.
//     This is what a fresh clone would check out, and it is the answer
//     almost everyone means.
//  3. The primary's HEAD, as a last resort, with the caller told that is
//     what happened -- it is a guess, and a guess worth naming.
//
// The returned ref is fetched before it is returned, so a branch is never
// cut from a stale local copy of a mainline that moved days ago.
func Base(ctx context.Context, r config.Repo, primary string) (ref string, guessed bool) {
	remote := r.RemoteOrDefault()

	if r.Base != "" {
		fetchRef(ctx, primary, remote, r.Base)
		// Prefer the just-fetched remote copy over a stale local branch of
		// the same name: the point of a base is that it is current.
		if refExists(ctx, primary, "refs/remotes/"+remote+"/"+r.Base) {
			return remote + "/" + r.Base, false
		}
		return r.Base, false
	}

	if def := remoteHead(ctx, primary, remote); def != "" {
		fetchRef(ctx, primary, remote, strings.TrimPrefix(def, remote+"/"))
		return def, false
	}

	return "HEAD", true
}

// remoteHead reads refs/remotes/<remote>/HEAD, which git points at the
// remote's default branch when the clone recorded one.
func remoteHead(ctx context.Context, primary, remote string) string {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary,
		"symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func refExists(ctx context.Context, primary, ref string) bool {
	return exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary,
		"show-ref", "--verify", "--quiet", ref).Run() == nil
}

// fetchRef updates one branch from one remote. Failure is ignored on
// purpose: offline is a normal state, and refusing to create a worktree
// because a fetch failed would be worse than cutting from a slightly stale
// base. The caller still gets a usable ref either way.
func fetchRef(ctx context.Context, primary, remote, branch string) {
	_ = exec.CommandContext(ctx, "git", "-C", primary,
		"fetch", "--quiet", remote, branch).Run()
}

// DescribeBase renders a base for a human, naming the guess when it is one.
func DescribeBase(ref string, guessed bool) string {
	if guessed {
		return fmt.Sprintf("%s (the primary checkout's current branch — no base configured and no remote default)", ref)
	}
	return ref
}

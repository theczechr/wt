package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
)

// fetchTimeout bounds the one-branch fetch `wt <branch>` runs on every open.
// mutateTimeout is sized for post_create installs; an unreachable remote
// must not hold the shell for that long when a local copy is right there.
const fetchTimeout = 30 * time.Second

// refreshBranch fetches one branch into refs/remotes/<remote>/<branch>.
//
// The refspec is explicit rather than left to remote.<name>.fetch: a clone
// made with --single-branch maps only its default branch, and a plain
// `git fetch <remote> <branch>` there updates FETCH_HEAD and nothing else,
// so the remote-tracking ref -- the one wt reads -- would stay stale. The
// leading '+' follows a force-pushed branch the way a default refspec does.
func refreshBranch(ctx context.Context, primary, remote, branch string) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	refspec := "+refs/heads/" + branch + ":refs/remotes/" + remote + "/" + branch
	out, err := exec.CommandContext(ctx, "git", "-C", primary, "fetch", "-q", remote, refspec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// fastForward brings the worktree at wt up to <remote>/<branch> when that
// loses nothing, which is what someone opening a branch almost always wants:
// the code as it is now, not as it was at their last fetch.
//
// A branch with commits of its own is left alone and the user is told: that
// is a pull or rebase, and choosing between them is theirs. Uncommitted
// changes are NOT a reason to skip -- `merge --ff-only` already refuses when
// one would be overwritten, tracked or untracked, and carries the rest
// across, exactly as `git pull --ff-only` does.
func fastForward(ctx context.Context, wt, remote, branch string, stderr io.Writer) {
	upstream := remote + "/" + branch
	behind, ok := canFastForward(ctx, wt, "HEAD", upstream, stderr)
	if !ok {
		return
	}
	old, err := headOf(ctx, wt)
	if err != nil {
		return
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", wt, "merge", "--ff-only", "-q", upstream).CombinedOutput(); err != nil {
		fmt.Fprintf(stderr, "wt: %s has %s, but could not fast-forward: %s\n",
			upstream, commits(behind), bytes.TrimSpace(out))
		return
	}
	fmt.Fprintf(stderr, "wt: fast-forwarded %s from %s\n", commits(behind), upstream)

	// Pulled commits are not the user's work. Advance the marker's start
	// only when nothing was committed since it, though: moving it past the
	// user's own commits would erase them from the reaper's count and let it
	// collect a worktree it should keep. Leaving it in place errs the other
	// way -- pulled commits by the same author count as own -- which only
	// keeps a worktree longer.
	if m, merr := ephemeral.ReadMarker(wt); merr == nil && m.Start == old {
		if head, herr := headOf(ctx, wt); herr == nil {
			if err := ephemeral.UpdateMarker(wt, func(m *ephemeral.Marker) { m.Start = head }); err != nil {
				fmt.Fprintf(stderr, "wt: fast-forwarded, but could not advance the marker's start commit: %v\n", err)
			}
		}
	}
}

// fastForwardRef moves refs/heads/<branch> up to <remote>/<branch> under the
// same rule as fastForward. Only for a branch checked out in no worktree:
// it moves the ref alone, so a checkout of it would be left out of step.
func fastForwardRef(ctx context.Context, primary, remote, branch string, stderr io.Writer) {
	local := "refs/heads/" + branch
	upstream := remote + "/" + branch
	behind, ok := canFastForward(ctx, primary, local, upstream, stderr)
	if !ok {
		return
	}
	old, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary, "rev-parse", local).Output()
	if err != nil {
		return
	}
	// The old value makes the update a compare-and-swap, so a commit landing
	// on the branch between the count and here is never thrown away.
	if out, err := exec.CommandContext(ctx, "git", "-C", primary, "update-ref",
		local, "refs/remotes/"+upstream, strings.TrimSpace(string(old))).CombinedOutput(); err != nil {
		fmt.Fprintf(stderr, "wt: %s has %s, but could not fast-forward %s: %s\n",
			upstream, commits(behind), branch, bytes.TrimSpace(out))
		return
	}
	fmt.Fprintf(stderr, "wt: fast-forwarded %s from %s\n", commits(behind), upstream)
}

// canFastForward reports how far rev is behind upstream, and whether moving
// it there is a fast-forward worth making: behind, and nothing of its own.
// It tells the user when a branch has diverged, since that is the case they
// would otherwise only discover by being puzzled at old code.
func canFastForward(ctx context.Context, dir, rev, upstream string, stderr io.Writer) (int, bool) {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", dir,
		"rev-list", "--left-right", "--count", rev+"..."+upstream).Output()
	if err != nil {
		// No remote copy (a local-only branch): nothing to follow.
		return 0, false
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, false
	}
	ahead, aerr := strconv.Atoi(fields[0])
	behind, berr := strconv.Atoi(fields[1])
	if aerr != nil || berr != nil || behind == 0 {
		return 0, false
	}
	if ahead > 0 {
		fmt.Fprintf(stderr, "wt: %s has %s your branch lacks, and your branch has %s of its own; left as is\n",
			upstream, commits(behind), commits(ahead))
		return 0, false
	}
	return behind, true
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return strconv.Itoa(n) + " commits"
}

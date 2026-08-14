package discover

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	"github.com/theczechr/wt/internal/model"
)

// ParseStatus counts changed files in `git status --porcelain` output.
func ParseStatus(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// ParseAheadBehind reads `git rev-list --left-right --count @{u}...HEAD`
// output, which is "<behind>\t<ahead>" — note git prints the left side first.
func ParseAheadBehind(out string) (ahead, behind int) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return 0, 0
	}
	behind, _ = strconv.Atoi(fields[0])
	ahead, _ = strconv.Atoi(fields[1])
	return ahead, behind
}

// ParseStatusV2 reads `git status --porcelain=v2 --branch` output, which
// carries both the dirty-file list and the ahead/behind counts in one call.
// The `# branch.ab +A -B` header line is absent entirely when the branch has
// no upstream (a normal, common case here) -- that yields ahead=0, behind=0,
// same as ParseAheadBehind on empty input, and hasUpstream=false. Every
// non-header line is one changed path.
//
// hasUpstream is true if and only if a `# branch.ab` line was actually seen
// -- it is NOT inferred from ahead/behind being non-zero, since "0 ahead, 0
// behind" is what a genuinely in-sync upstream branch looks like too. This
// is the one bit that lets a caller tell "never pushed anywhere" apart from
// "fully in sync with the remote", which ahead/behind alone cannot: both
// read as 0/0.
func ParseStatusV2(out string) (dirty, ahead, behind int, hasUpstream bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "# branch.ab "); ok {
			hasUpstream = true
			fields := strings.Fields(rest)
			if len(fields) == 2 {
				ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		dirty++
	}
	return dirty, ahead, behind, hasUpstream
}

// FillStatus populates DirtyCount, Ahead, and Behind with a single git
// invocation (`status --porcelain=v2 --branch`) instead of the two
// (`status --porcelain` plus `rev-list --left-right --count`) this used to
// run. A worktree with no upstream is normal, not an error.
// --no-optional-locks keeps this read-only call from contending on
// index.lock when dozens of these run concurrently against the same repo
// (git's documented fix for read-only callers).
//
// Measured on this machine: `git status` (in either form) can fail for
// reasons that have nothing to do with ahead/behind -- one real worktree
// here fails it outright because of a submodule misconfiguration (a
// submodule checked out where git expects a plain directory) -- while
// `rev-list --left-right --count` against the same worktree
// still succeeds. The old two-call FillStatus got ahead/behind from that
// independent call even when status failed; folding both into one call
// would otherwise silently lose that data for every worktree in this state.
// So a failed combined call falls back to the rev-list-only call rather
// than leaving Ahead/Behind zeroed when they didn't have to be -- the
// common case (a worktree where status succeeds) still costs exactly one
// fork.
//
// w.StatusKnown is set true ONLY on the success path above. When status
// fails, DirtyCount is never assigned -- it keeps whatever zero value it
// started with -- so StatusKnown is what tells PruneBlockers this is a
// genuinely unknown tree, not a clean one, even though the fallback below
// may still recover Ahead/Behind independently.
//
// w.HasUpstream is set true on the success path exactly when ParseStatusV2
// saw a `# branch.ab` line, and true on the fallback path exactly when the
// fallback's own `@{u}` resolved (that command errors outright when there
// is no upstream, so a genuinely-unpushed branch leaves HasUpstream at its
// zero value, false, here too -- both paths agree). Its zero value is
// otherwise left alone on any failure, which is the correct "warn" default
// -- see model.Workspace.HasUpstream.
func FillStatus(ctx context.Context, w *model.Workspace) {
	out, err := exec.CommandContext(ctx,
		"git", "--no-optional-locks", "-C", w.Path, "status", "--porcelain=v2", "--branch").Output()
	if err == nil {
		w.DirtyCount, w.Ahead, w.Behind, w.HasUpstream = ParseStatusV2(string(out))
		w.StatusKnown = true
		return
	}
	if out, err := exec.CommandContext(ctx,
		"git", "--no-optional-locks", "-C", w.Path, "rev-list", "--left-right", "--count", "@{u}...HEAD").Output(); err == nil {
		w.Ahead, w.Behind = ParseAheadBehind(string(out))
		// @{u} resolved, so an upstream genuinely is configured here --
		// combined status failed for some unrelated reason (corrupted
		// index, a submodule symlink git refuses to read, ...), not
		// because this is an unpushed branch. See
		// TestFillStatusFallsBackToRevListWhenStatusFails.
		w.HasUpstream = true
	}
}

// LocalOnlyCommitCount returns how many commits reachable from HEAD are not
// reachable from any remote-tracking ref -- i.e. commits that exist nowhere
// but this branch. It exists for the delete-confirmation's no-upstream
// warning: HasUpstream false could mean the branch was simply never pushed,
// and this is exactly the count of commits that fact puts at risk.
//
// `--not --remotes` is used rather than a plain `rev-list --count HEAD`
// because the latter counts the branch's ENTIRE history, including
// whatever it shares with develop/main -- for an ordinary feature branch
// that wildly overstates "local-only" (a branch with 3 new commits on top
// of 4,000 shared ones would report 4,003). It's used rather than diffing
// against a guessed "default branch" name because this codebase has no
// existing notion of one -- resolving it reliably needs its own git call
// and can still guess wrong -- while `--not --remotes` answers the actual
// question ("is this commit reachable from anywhere already pushed")
// directly, with the same single fork. When a repo has no remote
// configured at all, `--remotes` matches nothing and this correctly
// degrades to the full HEAD count: with no remote, the entire history
// really is local-only.
//
// ok is false on any git failure. This must never block or error out a
// delete confirmation, so the caller degrades to unqualified wording
// ("commits exist only here" with no number) rather than treating a
// failure here as anything more than "count unknown".
//
// Deliberately not called from aggregate.Collect: it forks a process, and
// running it once per worktree on every startup/refresh would undo the
// "one fork per worktree" budget FillStatus was optimised down to (see its
// own comment above). Call this lazily, only for the single worktree a
// delete confirmation is armed on.
func LocalOnlyCommitCount(ctx context.Context, path string) (int, bool) {
	out, err := exec.CommandContext(ctx,
		"git", "--no-optional-locks", "-C", path, "rev-list", "--count", "HEAD", "--not", "--remotes").Output()
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return n, true
}

// Package discover gathers worktrees, git state, sessions, and processes
// that feed model.Workspace.PruneBlockers, the gate deciding whether a
// worktree may be deleted. Every collector here is read by that gate,
// directly or through the Workspace it fills in, so the contract below is
// not style guidance -- it is what keeps PruneBlockers from authorising a
// deletion it cannot actually vouch for.
//
// The invariant: a collector that cannot read its input must not return
// the value that means "safe." A failed git status, a failed ps, a failed
// ReadDir must never collapse to the same zero value a genuinely
// clean/empty/absent result would produce -- that zero value is exactly
// what tells PruneBlockers nothing is wrong.
//
// This is three-way, not two-way:
//
//   - success: the real value -- a clean tree, N sessions, N processes.
//   - absent: also a real value, and it is benign. A worktree that never
//     hosted a Claude session genuinely has zero sessions; os.IsNotExist
//     reporting zero there is correct, not a fail-open. Blocking on
//     IsNotExist too would refuse every worktree nobody ever opened
//     Claude in -- precisely the ones most safely deletable, and a worse
//     outcome than the bug this invariant guards against.
//   - unreadable: unknown, and unknown must never be reported as safe. A
//     failed exec, a permission error, a timeout carry no information
//     about the real state, so none of them may produce the same value a
//     genuine "nothing here" would.
//
// Four instances of this defect have been found in this package. All four
// are now closed; each is kept below because the shape recurs, and the
// fixes are the worked examples:
//
//  1. FillStatus set DirtyCount = 0 when `git status` failed -- the same
//     zero an actually-clean tree produces. One real worktree in this
//     checkout fails status outright -- a submodule misconfiguration in
//     that checkout makes git refuse to report status at all -- and would
//     have read as clean. Fixed: see StatusKnown below.
//  2. ParseStatusV2 read a missing `# branch.ab` line as "0 ahead, 0
//     behind" -- indistinguishable from a branch fully in sync with its
//     upstream, when the actual case is a branch with NO upstream at
//     all. Fixed: see HasUpstream below.
//  3. Snapshot returned an empty process list when `ps` failed --
//     indistinguishable from nothing actually running. Fixed by
//     SnapshotErr, which returns a Processes{Procs, Live, SessionProcs}
//     struct plus an error; Snapshot itself is kept, unchanged, for
//     callers that have always treated "nothing running" and "couldn't
//     check" the same way and must keep doing so.
//  4. SessionsFor returned nil on ANY os.ReadDir error --
//     indistinguishable from a worktree that genuinely has no sessions.
//     Fixed: it now returns ([]Session, error), and the split is the
//     three-way one above rather than a plain success/failure pair. A
//     missing project directory is NOT an error -- a worktree nobody ever
//     opened Claude in really does have zero sessions, and that is the
//     common case -- while permission and I/O failures are. Callers record
//     the outcome in model.Workspace.SessionsKnown, which PruneBlockers
//     treats as a blocker when false. That was the last of the four.
//
// The three fixes above share a pattern worth copying: each adds a signal
// recording whether the read actually succeeded, and makes the caller
// check that signal rather than infer success from the payload.
// StatusKnown is true only once FillStatus's status call (or its
// rev-list fallback) actually completed. HasUpstream is true only when a
// `# branch.ab` line was actually observed, or the fallback's `@{u}`
// genuinely resolved -- never inferred from Ahead/Behind being zero,
// since an in-sync upstream and a missing one both produce 0/0.
// SnapshotErr returns an explicit error alongside the process lists,
// instead of swallowing it. A new collector should follow the same
// shape: add a "did this actually succeed" signal next to the value, not
// instead of it.
//
// Polarity matters as much as presence: the zero value of that signal
// must be the unsafe-to-proceed state. StatusKnown, never StatusUnknown.
// A Workspace{} literal -- from a test, from a pre-migration snapshot.json
// entry missing the field, from any code path that simply forgot to
// populate it -- has StatusKnown == false by construction, and
// PruneBlockers treats that as a blocker. Named the other way round, that
// same uninitialised struct would read as "known safe" and silently
// authorise deletion. Every boolean this package adds for this purpose
// must default, unpopulated, to the state that refuses -- fail closed,
// never fail open. See model.Workspace's comments on StatusKnown and
// HasUpstream, and model_test.go's
// TestPruneBlockersZeroValueWorkspaceIsBlocked, which pins this down as a
// regression test.
package discover

import (
	"context"
	"os/exec"
	"strings"

	"github.com/theczechr/wt/internal/model"
)

func classify(path, primary string) model.Kind {
	switch {
	case path == primary:
		return model.KindPrimary
	case strings.HasPrefix(path, primary+"/.claude/worktrees/"):
		return model.KindClaudeManaged
	case strings.HasPrefix(path, primary+"/.worktrees/"):
		return model.KindNested
	case strings.HasPrefix(path, primary+"/"):
		return model.KindNested
	}
	// A sibling lives in the same parent directory as the primary checkout.
	cut := strings.LastIndex(primary, "/")
	if cut > 0 && strings.HasPrefix(path, primary[:cut+1]) {
		return model.KindSibling
	}
	return model.KindForeign
}

// ParseWorktreeList parses `git worktree list --porcelain` output. Records are
// separated by blank lines; a detached worktree has no branch line.
func ParseWorktreeList(out, repo, primary string) []model.Workspace {
	var res []model.Workspace
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var w model.Workspace
		w.Repo = repo
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				w.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "HEAD "):
				w.Head = strings.TrimPrefix(line, "HEAD ")
			case strings.HasPrefix(line, "branch "):
				w.Branch = strings.TrimPrefix(
					strings.TrimPrefix(line, "branch "), "refs/heads/")
			}
		}
		if w.Path == "" {
			continue
		}
		w.Kind = classify(w.Path, primary)
		res = append(res, w)
	}
	return res
}

// Worktrees runs git in repoPath and returns every worktree of that repo,
// wherever on disk it lives.
func Worktrees(ctx context.Context, repoPath, repoName string) ([]model.Workspace, error) {
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(string(out), repoName, repoPath), nil
}

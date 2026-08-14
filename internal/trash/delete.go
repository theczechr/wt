package trash

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/theczechr/wt/internal/bootstrap"
	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/model"
)

// BlockedError reports that model.PruneBlockers refused a deletion attempt.
// It is the ONLY reason SoftDelete or HardDelete refuse before touching
// git: the primary checkout, uncommitted changes, a live Claude session, or
// running processes.
type BlockedError struct{ Blockers []string }

func (e *BlockedError) Error() string {
	return "refusing to delete: " + strings.Join(e.Blockers, "; ")
}

// SoftDelete is the default deletion mode. `git worktree remove` -- never
// --force, so git's own refusal on uncommitted changes stays a second,
// independent safety layer under PruneBlockers -- removes the checkout and
// frees disk immediately. The directory only ever disappears through this
// one `git worktree remove` call: nothing in this package ever walks or
// os.RemoveAll's a worktree's own file tree itself, so a symlink a worktree
// contains (e.g. the .env files `wt bootstrap` links back to the primary
// checkout) is unlinked by git exactly like os.RemoveAll would, never
// followed and deleted through. See TestSoftDeleteDoesNotFollowSymlinkOutOfWorktree.
//
// The branch is left untouched, and a manifest entry is recorded so
// <space>t can rebuild the worktree later via Restore.
//
// ws.PruneBlockers() is consulted first and is the ONLY gate: primary
// checkouts, dirty worktrees, live sessions and running processes are all
// refused here before any git command runs.
func SoftDelete(ctx context.Context, ws model.Workspace, primary string) (Entry, error) {
	if blockers := ws.PruneBlockers(); len(blockers) > 0 {
		return Entry{}, &BlockedError{Blockers: blockers}
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", primary, "worktree", "remove", ws.Path).CombinedOutput(); err != nil {
		return Entry{}, fmt.Errorf("git worktree remove: %w: %s", err, bytes.TrimSpace(out))
	}
	e := Entry{
		Path:      ws.Path,
		Repo:      ws.Repo,
		Branch:    ws.Branch,
		Primary:   primary,
		DeletedAt: time.Now(),
	}
	if err := Add(e); err != nil {
		return e, fmt.Errorf("worktree removed but failed to record a trash entry: %w", err)
	}
	return e, nil
}

// HardDeleteResult reports what happened to the branch after a hard
// delete's worktree removal succeeded.
type HardDeleteResult struct {
	BranchDeleted bool
	// BranchKeptReason is git's own refusal message, non-empty exactly when
	// BranchDeleted is false.
	BranchKeptReason string
}

// HardDelete removes the worktree exactly like SoftDelete (same
// PruneBlockers gate, same non---force `git worktree remove`), then
// additionally attempts `git branch -d` -- the SAFE delete, which refuses
// an unmerged branch. HardDelete NEVER escalates to `git branch -D`: if git
// refuses, that refusal is reported back to the caller via
// HardDeleteResult and the branch is left exactly as it was.
//
// No trash entry is recorded for a hard delete: the branch may still be
// alive (a refused -d), so there would be nothing consistent for Restore to
// rebuild from, and when the branch IS gone there is nothing left to
// restore to in the first place.
func HardDelete(ctx context.Context, ws model.Workspace, primary string) (HardDeleteResult, error) {
	if blockers := ws.PruneBlockers(); len(blockers) > 0 {
		return HardDeleteResult{}, &BlockedError{Blockers: blockers}
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", primary, "worktree", "remove", ws.Path).CombinedOutput(); err != nil {
		return HardDeleteResult{}, fmt.Errorf("git worktree remove: %w: %s", err, bytes.TrimSpace(out))
	}
	out, err := exec.CommandContext(ctx, "git", "-C", primary, "branch", "-d", ws.Branch).CombinedOutput()
	if err != nil {
		return HardDeleteResult{BranchKeptReason: strings.TrimSpace(string(out))}, nil
	}
	return HardDeleteResult{BranchDeleted: true}, nil
}

// Restore re-creates a trashed worktree at its original path on its
// recorded branch (`git worktree add <path> <branch>` -- the branch
// already exists, since SoftDelete never deletes it, so this is never given
// -b), then re-runs the same bootstrap.Run a fresh `wt new` would: env
// symlinks, submodules, post_create hooks.
//
// A path that is now occupied (something else recreated it, or a stale
// leftover) is refused outright rather than risking `git worktree add`
// colliding with it.
//
// The manifest entry is removed as soon as `git worktree add` succeeds --
// at that point the directory is real again and the trash entry would
// otherwise describe a path that no longer matches reality -- even if the
// following bootstrap step then fails; a bootstrap failure is reported to
// the caller exactly like `wt new`'s own does, and the now-real worktree is
// left in place for the user to bootstrap by hand.
func Restore(ctx context.Context, e Entry, repoCfg config.Repo) error {
	if _, err := os.Stat(e.Path); err == nil {
		return fmt.Errorf("refusing to restore: %s already exists", e.Path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("refusing to restore: %s: %w", e.Path, err)
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", e.Primary, "worktree", "add", e.Path, e.Branch).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, bytes.TrimSpace(out))
	}
	if err := Remove(e); err != nil {
		return fmt.Errorf("restored %s but failed to update the trash manifest: %w", e.Path, err)
	}
	if err := bootstrap.Run(ctx, e.Primary, e.Path, repoCfg); err != nil {
		return fmt.Errorf("restored %s but bootstrap failed: %w", e.Path, err)
	}
	return nil
}

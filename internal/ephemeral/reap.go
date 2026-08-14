package ephemeral

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/discover"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/trash"
)

// state is one inspection of a worktree: the verdict, plus the git and
// process facts that produced it.
//
// The Workspace is carried rather than recomputed because Reap hands it to
// trash.SoftDelete, which re-checks model.PruneBlockers against its fields.
// That re-check is a genuine second layer only if the fields are real; a
// zero-valued Workspace would make PruneBlockers return nothing and quietly
// turn the layer into decoration.
type state struct {
	marker   Marker
	ws       model.Workspace
	blockers []string
}

// Blockers lists every reason worktree must not be removed. An empty result
// permits removal; anything else forbids it.
func Blockers(ctx context.Context, cfg config.Config, worktree string) []string {
	return BlockersExcluding(ctx, cfg, worktree, "")
}

// BlockersExcluding is Blockers with one Claude session treated as already
// over. See Reap for why that exemption exists and why it is scoped to a
// single id.
func BlockersExcluding(ctx context.Context, cfg config.Config, worktree, excludeSessionID string) []string {
	return inspect(ctx, cfg, worktree, excludeSessionID).blockers
}

// inspect gathers the worktree's state once and judges it.
//
// The clauses are deliberately redundant. The marker proves wt created the
// worktree, and containment proves it sits where wt puts them; either alone
// would be enough in a correct program, and requiring both means a single
// bug cannot authorise a deletion.
//
// excludeSessionID names one Claude session that must not count as live --
// empty for every caller but the SessionEnd hook. See Reap.
func inspect(ctx context.Context, cfg config.Config, worktree, excludeSessionID string) state {
	m, err := ReadMarker(worktree)
	if err != nil {
		return state{blockers: []string{fmt.Sprintf("no usable %s marker: %v", MarkerName, err)}}
	}
	r, ok := cfg.Repos[m.Repo]
	if !ok {
		return state{marker: m, blockers: []string{
			fmt.Sprintf("repo %q from the marker is not configured", m.Repo)}}
	}

	var b []string
	ws := model.Workspace{
		Repo:   m.Repo,
		Path:   worktree,
		Branch: m.Branch,
		Kind:   model.KindNested,
	}

	root := filepath.Join(m.Primary, r.EphemeralDirOrDefault())
	if !strictlyWithin(root, worktree) {
		b = append(b, fmt.Sprintf("%s is not inside the ephemeral dir %s", worktree, root))
	}
	if strictlyWithin(worktree, m.Primary) || sameDir(worktree, m.Primary) {
		b = append(b, "refusing to remove the primary checkout")
	}

	if !registeredWorktree(ctx, m.Primary, worktree) {
		b = append(b, fmt.Sprintf("%s is not a registered worktree of %s", worktree, m.Primary))
	}

	dirty, err := dirtyCount(ctx, worktree)
	if err != nil {
		// Not "assume clean": a status that cannot be read is a state we
		// know nothing about, and the only safe reading of nothing is "do
		// not delete". One real worktree in this checkout fails status
		// outright because of a submodule misconfiguration, proving this
		// is not a hypothetical.
		// DirtyCount is deliberately left at 0 in this case -- it is a count,
		// not a verdict, and the verdict is already recorded in b.
		b = append(b, fmt.Sprintf("could not read git status: %v", err))
	} else {
		// This is the one path on which the status read actually succeeded, so
		// it is the only place StatusKnown may be set. Its zero value means
		// "could not be read" and model.PruneBlockers refuses on it -- which is
		// what makes trash.SoftDelete a real second gate under this predicate
		// rather than a vacuous one, and what stops a bare Workspace{} literal
		// anywhere else from ever reading as "known clean".
		ws.StatusKnown = true
		ws.DirtyCount = dirty
		if dirty > 0 {
			b = append(b, "has uncommitted changes")
		}
	}

	hasUpstream, ahead, err := upstreamState(ctx, worktree)
	// Recorded on every path, including the error one, where it stays false --
	// the same fail-closed polarity as StatusKnown. The delete confirmation
	// reads it to warn about never-pushed work, and this is the only place it
	// is computed, so leaving it unset would silently understate the risk.
	ws.HasUpstream = hasUpstream
	switch {
	case err != nil:
		b = append(b, fmt.Sprintf("could not read upstream state: %v", err))
	case !hasUpstream:
		// Ahead would read 0 here, indistinguishable from fully-pushed, while
		// meaning every commit on this branch exists nowhere else.
		b = append(b, "branch has no upstream, so nothing on it is known to be pushed")
	case ahead > 0:
		ws.Ahead = ahead
		b = append(b, fmt.Sprintf("has %d unpushed commit(s)", ahead))
	}

	// The marker's Branch is written once, at creation, and never updated.
	// If someone switches branches inside the ephemeral (git switch/checkout
	// -b), the live checkout and the marker disagree, and ws.Branch above
	// would still carry the stale marker value straight into the trash
	// entry. trash.Restore rebuilds with `git worktree add <path> <branch>`
	// from that recorded branch, so a stale value would restore the wrong
	// branch or fail outright -- exactly the unrestorable entry dropping
	// ephemeral_delete_branch was meant to avoid. A disagreement means the
	// worktree is no longer what the marker says it is, so it is refused
	// rather than trusted either way.
	switch branch, err := currentBranch(ctx, worktree); {
	case err != nil:
		b = append(b, fmt.Sprintf("could not read the checked-out branch: %v", err))
	case branch != m.Branch:
		b = append(b, fmt.Sprintf("checked-out branch %q does not match the marker's %q", branch, m.Branch))
	}

	// One snapshot, feeding both checks and the Workspace. Snapshot forks ps
	// and lsof, so calling it twice would double the cost of every reap for
	// no new information. SnapshotErr (not Snapshot) is used deliberately: a
	// failed ps must read as "couldn't check", never as "nothing running" --
	// the same FillStatus-shaped trap this task exists to close, reproduced
	// here for processes instead of git status.
	snap, snapErr := discover.SnapshotErr(ctx)
	if snapErr != nil {
		b = append(b, fmt.Sprintf("could not enumerate processes: %v", snapErr))
	}

	// worktree can legitimately reach here in two forms: the one wt created
	// it with, and the OS-resolved form lsof and a shell hand back (macOS
	// resolves /var to /private/var, and t.TempDir() sits under it). Claude
	// Code's session directory name is a literal character-for-character
	// flatten of whichever cwd form the user actually typed (ProjectDirName),
	// so only one form may have a matching directory; query both and dedupe
	// by session ID rather than guessing which one applies here.
	rw := resolve(worktree)
	queryPaths := []string{worktree}
	if rw != worktree {
		queryPaths = append(queryPaths, rw)
	}
	seenSession := map[string]bool{}
	for _, qp := range queryPaths {
		for _, s := range discover.SessionsFor(qp, nil) {
			if seenSession[s.ID] || s.ID == excludeSessionID {
				continue
			}
			seenSession[s.ID] = true
			if pid, running := snap.Live[s.ID]; running {
				s.Live, s.PID = true, pid
			}
			ws.Sessions = append(ws.Sessions, s)
		}
	}

	// Transcript discovery above is necessary but nowhere near sufficient. It
	// finds a session only when ~/.claude/projects holds a directory whose
	// name is a flatten of the worktree root, and there are three ordinary
	// ways a live session has no such directory: it was launched from a
	// subdirectory (the spec explicitly allows cwd to be one, and the name is
	// then a flatten of THAT path); it has not written its first message yet,
	// so the directory does not exist; or ~/.claude/projects is unreadable and
	// SessionsFor's ReadDir error becomes a nil slice. All three read as "no
	// session" and authorise a deletion -- the same failed-probe-means-safe
	// trap already closed for git status and for ps.
	//
	// The process table has none of those blind spots: wt has already resolved
	// the cwd of every Claude process, so a session running at or under this
	// worktree is a fact, not an inference. Matched with the same
	// resolved-to-resolved comparison as the process loop below, so a
	// subdirectory is handled identically.
	//
	// Appended to ws.Sessions rather than only to b: trash.SoftDelete re-checks
	// model.PruneBlockers, and that second layer is only real if the Workspace
	// carries the sessions this one found.
	for _, sp := range snap.SessionProcs {
		if seenSession[sp.SessionID] || sp.SessionID == excludeSessionID {
			continue
		}
		pc := resolve(sp.Cwd)
		if pc != rw && !strings.HasPrefix(pc, rw+string(filepath.Separator)) {
			continue
		}
		seenSession[sp.SessionID] = true
		ws.Sessions = append(ws.Sessions, model.Session{
			ID: sp.SessionID, Cwd: sp.Cwd, Live: true, PID: sp.PID,
		})
	}
	if ws.HasLiveSession() {
		b = append(b, "has a live Claude session")
	}
	// p.Cwd, from discover.CwdOf's lsof call, is the resolved vnode path, not
	// necessarily the form worktree was given in -- compare resolved to
	// resolved like every other path comparison in this file (see resolve's
	// own doc comment), not the raw strings.
	for _, p := range snap.Procs {
		pc := resolve(p.Cwd)
		if pc == rw || strings.HasPrefix(pc, rw+string(filepath.Separator)) {
			ws.Procs = append(ws.Procs, p)
		}
	}
	if len(ws.Procs) > 0 {
		b = append(b, fmt.Sprintf("has a running process: %s", ws.Procs[0].Command))
	}

	return state{marker: m, ws: ws, blockers: b}
}

// Reap removes worktree if and only if Blockers is empty.
//
// Removal goes through trash.SoftDelete rather than a direct `git worktree
// remove`: a reap is unattended, so it must leave a manifest entry the user
// can restore from. SoftDelete's own model.PruneBlockers check is a second
// layer under the inspection above, never a substitute for it -- it has no
// upstream or ahead check, and the DirtyCount it reads is 0 both for a clean
// tree and for one whose `git status` failed.
//
// delete_mode is deliberately not consulted. It governs the dashboard's
// manual delete, where the user is present to catch a mistake; nothing
// watches a reap, so it always soft-deletes.
//
// excludeSessionID exempts exactly one Claude session from the live-session
// clause, and exists because a SessionEnd hook runs while the session that
// fired it is still alive -- Claude forks `wt reap` and waits for it, so `ps`
// still lists the ending session and its transcript, by definition, has been
// written. Without the exemption the hook would refuse every reap it was
// wired up to perform, and the reap log would fill with what looks like a
// bug in the guard. The exemption is one id, never a flag: any OTHER live
// session in the worktree still blocks, which is the case that matters (a
// second terminal working in the same tree). Callers with no such session --
// Sweep, and `wt reap <path>` typed by hand -- pass "".
func Reap(ctx context.Context, cfg config.Config, worktree, excludeSessionID string) error {
	st := inspect(ctx, cfg, worktree, excludeSessionID)
	if len(st.blockers) > 0 {
		return fmt.Errorf("refusing to remove %s: %s", worktree, strings.Join(st.blockers, "; "))
	}
	if _, err := trash.SoftDelete(ctx, st.ws, st.marker.Primary); err != nil {
		return fmt.Errorf("soft-deleting %s: %w", worktree, err)
	}
	return nil
}

// dirtyCount counts changed paths in the worktree. The error return is not
// incidental: callers must distinguish "clean" from "unknown", and this is
// exactly where discover.FillStatus goes wrong by collapsing both to 0.
func dirtyCount(ctx context.Context, worktree string) (int, error) {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", worktree,
		"status", "--porcelain").Output()
	if err != nil {
		return 0, withStderr(err)
	}
	return discover.ParseStatus(string(out)), nil
}

// withStderr folds git's own diagnostic into the error. exec.Cmd.Output()
// captures stderr into ExitError.Stderr but leaves the error text at "exit
// status 128", and these three errors are the entire content of a refusal
// the operator reads in reap.log. The spec names the real case: a worktree in
// this checkout whose status fails outright because of a submodule
// misconfiguration. Told only the exit status, nobody can tell that
// apart from a corrupt gitdir or a stale index.lock, and all three want
// different responses.
func withStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return err
}

// currentBranch reads the branch actually checked out in worktree, straight
// from git -- never from the marker, which is written once at creation and
// never updated, so it cannot see a later `git switch` inside the worktree.
func currentBranch(ctx context.Context, worktree string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", worktree,
		"symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", withStderr(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// upstreamState reports whether the checked-out branch has an upstream and
// how many commits it is ahead of it. The two are returned together because
// neither is meaningful alone: ahead == 0 with no upstream means "nothing is
// pushed", the opposite of what it means with one.
func upstreamState(ctx context.Context, worktree string) (hasUpstream bool, ahead int, err error) {
	if err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", worktree,
		"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").Run(); err != nil {
		// An absent upstream is a normal state, not a failure to read one.
		return false, 0, nil
	}
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", worktree,
		"rev-list", "--count", "@{u}..HEAD").Output()
	if err != nil {
		return true, 0, withStderr(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return true, 0, fmt.Errorf("unreadable ahead count %q: %w", out, err)
	}
	return true, n, nil
}

// registeredWorktree reports whether git itself considers worktree one of
// primary's worktrees, catching a stale directory left behind after a manual
// removal.
func registeredWorktree(ctx context.Context, primary, worktree string) bool {
	ws, err := discover.Worktrees(ctx, primary, "")
	if err != nil {
		return false
	}
	for _, w := range ws {
		if sameDir(w.Path, worktree) {
			return true
		}
	}
	return false
}

// strictlyWithin reports whether path lies inside base and is not base
// itself. bootstrap has a similar helper, deliberately not reused: its
// version treats base == path as contained, which is exactly the case that
// must be refused here.
func strictlyWithin(base, path string) bool {
	base, path = resolve(base), resolve(path)
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sameDir(a, b string) bool { return resolve(a) == resolve(b) }

// resolve absolutises and dereferences a path so comparisons survive
// symlinked parents -- on macOS /tmp is itself a symlink, so tests and real
// runs both depend on this.
func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

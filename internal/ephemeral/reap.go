package ephemeral

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/model"
	"github.com/theczechr/ephemera/internal/trash"
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
	verdict  Verdict
	// hasSubmodules selects `git worktree remove --force` (spec §4.1).
	hasSubmodules bool
	// deleteBranch is true when the branch is wt's own and fully on a
	// remote (spec §4.2). Set by Task 7.
	deleteBranch bool
}

// DefaultSweepGrace protects a worktree created or opened this recently
// from sweeps. Without it, the sweep `wt <branch>` itself triggers could
// remove the worktree it just made. The SessionEnd hook and `wt reap
// <path>` apply none.
const DefaultSweepGrace = 15 * time.Minute

// InspectOptions tunes one inspection.
type InspectOptions struct {
	// ExcludeSessionID is the one session treated as already over. See Reap.
	ExcludeSessionID string
	Grace            time.Duration
	// Now is "now" for the idle and grace checks; zero means time.Now().
	Now time.Time
}

// Decision is one worktree's verdict, as printed by `wt reap --dry-run`
// and logged to reap.log.
type Decision struct {
	Path     string   `json:"path"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Reap     bool     `json:"reap"`
	Reason   string   `json:"reason"`
	Blockers []string `json:"blockers,omitempty"`
}

func (st state) decision(path string) Decision {
	d := Decision{
		Path: path, Repo: st.marker.Repo, Branch: st.marker.Branch,
		Reap: len(st.blockers) == 0, Blockers: st.blockers,
	}
	if d.Reap {
		d.Reason = st.verdict.Reason
	} else {
		d.Reason = strings.Join(st.blockers, "; ")
	}
	return d
}

// Inspect judges worktree without touching it.
func Inspect(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) Decision {
	return inspect(ctx, cfg, worktree, opts).decision(worktree)
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
	return inspect(ctx, cfg, worktree, InspectOptions{ExcludeSessionID: excludeSessionID}).blockers
}

// inspect gathers the worktree's state once and judges it.
//
// The clauses are deliberately redundant. The marker proves wt created the
// worktree, and containment proves it sits where wt puts them; either alone
// would be enough in a correct program, and requiring both means a single
// bug cannot authorise a deletion.
//
// opts.ExcludeSessionID names one Claude session that must not count as live --
// empty for every caller but the SessionEnd hook. See Reap.
func inspect(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) state {
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

	switch locked, err := isLocked(ctx, m.Primary, worktree); {
	case err != nil:
		b = append(b, fmt.Sprintf("could not read the worktree's lock state: %v", err))
	case locked:
		b = append(b, "worktree is locked (git worktree lock)")
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

	switch dirty, err := submodulesDirty(ctx, worktree); {
	case err != nil:
		b = append(b, fmt.Sprintf("could not read submodule status: %v", err))
	case dirty:
		b = append(b, "a submodule has uncommitted or untracked changes")
	}
	b = append(b, submoduleCommitBlockers(ctx, worktree)...)
	hasSubs, err := hasSubmodules(ctx, worktree)
	if err != nil {
		b = append(b, fmt.Sprintf("could not read submodule state: %v", err))
	}

	// No longer a blocker (spec §2.1): the branch outlives the worktree, so
	// neither an upstream nor an ahead count protects committed work. Still
	// recorded, because the dashboard's delete confirmation reads them.
	hasUpstream, ahead, _ := upstreamState(ctx, worktree)
	ws.HasUpstream = hasUpstream
	ws.Ahead = ahead

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

	sessions, procs, sessionsKnown, liveBlockers := probeLiveness(ctx, worktree, opts.ExcludeSessionID)
	ws.Sessions, ws.Procs, ws.SessionsKnown = sessions, procs, sessionsKnown
	b = append(b, liveBlockers...)

	st := state{marker: m, ws: ws, blockers: b, hasSubmodules: hasSubs}
	if len(b) > 0 {
		return st
	}
	// Only a worktree that is SAFE to remove is asked whether it is DONE.
	own, err := OwnCommits(ctx, worktree, m)
	if err != nil {
		st.blockers = append(st.blockers, fmt.Sprintf("could not count your commits: %v", err))
		return st
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	f := Facts{
		OwnCommits: own,
		LastUsed:   LastUsed(ctx, worktree, m, ws.Sessions),
		OpenedAt:   openedAt(m),
		Grace:      opts.Grace,
		IdleDays:   cfg.EffectiveIdleDays(m.Repo),
		Now:        now,
	}
	if own > 0 {
		f.PRState = PRState(ctx, m.Primary, m)
	}
	st.verdict = Decide(f)
	if !st.verdict.Done {
		st.blockers = append(st.blockers, "not done yet: "+st.verdict.Reason)
		return st
	}
	if m.BranchCreatedByWT {
		// Unreadable means "keep the branch": the safe outcome, not a blocker.
		if n, err := unpushedCount(ctx, m.Primary, m.Branch); err == nil && n == 0 {
			st.deleteBranch = true
		}
	}
	return st
}

// probeLiveness reports every Claude session and process living at or under
// worktree, and the blockers they (or a failed probe) impose. It is its own
// function so ReapWith can run it a second time immediately before removal
// (spec §3.4): the slow PR lookup in inspect sits between the first probe and
// the removal, and a session started in that window must still block.
func probeLiveness(ctx context.Context, worktree, excludeSessionID string) (sessions []model.Session, procs []model.Proc, sessionsKnown bool, blockers []string) {
	// One snapshot, feeding both checks and the Workspace. Snapshot forks ps
	// and lsof, so it is taken once per probe; the probe itself is
	// deliberately repeated by ReapWith just before removal. SnapshotErr (not Snapshot) is used deliberately: a
	// failed ps must read as "couldn't check", never as "nothing running" --
	// the same FillStatus-shaped trap this task exists to close, reproduced
	// here for processes instead of git status.
	snap, snapErr := discover.SnapshotErr(ctx)
	if snapErr != nil {
		blockers = append(blockers, fmt.Sprintf("could not enumerate processes: %v", snapErr))
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
	sessionsUnreadable := false
	for _, qp := range queryPaths {
		found, err := discover.SessionsFor(qp, nil)
		if err != nil {
			// Unreadable, not empty. Recorded as a blocker outright: this
			// function's whole job is deciding whether a worktree may be
			// deleted, and a session probe that could not run says nothing
			// about what is living in it.
			blockers = append(blockers, "Claude sessions could not be read: "+err.Error())
			sessionsUnreadable = true
		}
		for _, s := range found {
			if seenSession[s.ID] || s.ID == excludeSessionID {
				continue
			}
			seenSession[s.ID] = true
			if pid, running := snap.Live[s.ID]; running {
				s.Live, s.PID = true, pid
			}
			sessions = append(sessions, s)
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
		sessions = append(sessions, model.Session{
			ID: sp.SessionID, Cwd: sp.Cwd, Live: true, PID: sp.PID,
		})
	}
	// A Claude process with no session id on its command line -- a plain
	// interactive `claude`, or a new T3 Code session -- has nothing to match
	// against a transcript or exempt by name, so any one at or under this
	// worktree counts as a live session. The single exception is the
	// SessionEnd hook's own session: Claude runs the hook as its descendant
	// and waits for it, so the NEAREST Claude process among this process's
	// ancestors is the session that is ending. Only that one is exempt: an
	// outer `claude` whose Bash tool started the ending one is still alive
	// and still blocks. The exemption needs the hook's ending-session id to
	// be set (sweeps and `wt reap <path>` never exempt anything), and a
	// failed ancestor walk exempts nothing.
	ending := 0
	if excludeSessionID != "" && len(snap.ClaudeProcs) > 0 {
		ending = nearestClaudeAncestor(ctx)
	}
	for _, p := range snap.ClaudeProcs {
		pc := resolve(p.Cwd)
		if pc != rw && !strings.HasPrefix(pc, rw+string(filepath.Separator)) {
			continue
		}
		if ending != 0 && p.PID == ending {
			continue
		}
		sessions = append(sessions, model.Session{Cwd: p.Cwd, Live: true, PID: p.PID})
	}
	// The Workspace handed to trash.SoftDelete re-runs PruneBlockers, whose
	// !SessionsKnown check is only meaningful if this probe's outcome
	// travels with it. Known exactly when every queried path answered.
	sessionsKnown = !sessionsUnreadable
	if (model.Workspace{Sessions: sessions}).HasLiveSession() {
		blockers = append(blockers, "has a live Claude session")
	}
	// p.Cwd, from discover.CwdOf's lsof call, is the resolved vnode path, not
	// necessarily the form worktree was given in -- compare resolved to
	// resolved like every other path comparison in this file (see resolve's
	// own doc comment), not the raw strings.
	for _, p := range snap.Procs {
		pc := resolve(p.Cwd)
		if pc == rw || strings.HasPrefix(pc, rw+string(filepath.Separator)) {
			procs = append(procs, p)
		}
	}
	if len(procs) > 0 {
		blockers = append(blockers, fmt.Sprintf("has a running process: %s", procs[0].Command))
	}
	return sessions, procs, sessionsKnown, blockers
}

// Reap is ReapWith with no grace and only ExcludeSessionID set. Removal
// behaviour lives in ReapWith; see it for the details.
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
	_, err := ReapWith(ctx, cfg, worktree, InspectOptions{ExcludeSessionID: excludeSessionID})
	return err
}

// ancestorChain is swapped out in tests (export_test.go).
var ancestorChain = discover.AncestorChain

// nearestClaudeAncestor returns the pid of the Claude process that started
// this one -- the session whose SessionEnd hook is running -- or 0.
//
// The walk goes up from wt's parent only through processes known to sit
// between a hook and its session: shells and wt itself. The first ancestor
// that is neither is the candidate, and it is returned only if it is a
// Claude process (with or without a session id). Anything else returns 0:
// taking the nearest ancestor merely RECOGNISED as claude would skip an
// unrecognised ending session (a `node cli.js`, a versioned binary) and
// exempt an outer live claude further up in the same worktree. A failed
// ancestry read also returns 0, so nothing is exempt.
func nearestClaudeAncestor(ctx context.Context) int {
	chain, err := ancestorChain(ctx)
	if err != nil {
		return 0
	}
	for _, a := range chain {
		if hookIntermediate(a.Command) {
			continue
		}
		if _, isSession := discover.IsClaudeSession(a.Command); isSession || discover.IsClaudeProcess(a.Command) {
			return a.PID
		}
		return 0
	}
	return 0
}

// hookIntermediate reports whether a command line is a process that may sit
// between a SessionEnd hook's claude and this wt: a shell (including a
// login shell's "-zsh" form) or wt itself.
func hookIntermediate(cmd string) bool {
	argv0, _, _ := strings.Cut(cmd, " ")
	base := strings.TrimPrefix(filepath.Base(argv0), "-")
	switch base {
	case "sh", "bash", "zsh", "dash", "fish", "wt":
		return true
	}
	if exe, err := os.Executable(); err == nil && base == filepath.Base(exe) {
		return true
	}
	return false
}

// afterRechecks is a test seam: called after the liveness and cleanliness
// rechecks, so a test can reopen the worktree in the slowest part of the
// removal window.
var afterRechecks = func(string) {}

// beforeRemoval is a test seam: called right before the final liveness
// re-probe, so a test can start a process inside the removal window.
var beforeRemoval = func(string) {}

// ReapWith removes worktree if and only if inspection finds no blockers. The
// inspection runs here, immediately before removal, never earlier (spec
// §3.4): a session opened mid-sweep still blocks.
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
func ReapWith(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) (Decision, error) {
	st := inspect(ctx, cfg, worktree, opts)
	d := st.decision(worktree)
	if !d.Reap {
		return d, fmt.Errorf("refusing to remove %s: %s", worktree, strings.Join(st.blockers, "; "))
	}
	beforeRemoval(worktree)
	if _, _, _, busy := probeLiveness(ctx, worktree, opts.ExcludeSessionID); len(busy) > 0 {
		d.Reap, d.Reason = false, "became busy before removal: "+strings.Join(busy, "; ")
		return d, fmt.Errorf("refusing to remove %s: %s", worktree, d.Reason)
	}
	// Cleanliness is re-read too (spec §3.4): inspect ran before the slow PR
	// lookup, and with --force git's own dirty refusal is gone, so a file
	// written in that window would be destroyed. Fail closed on any error.
	if changed := recheckClean(ctx, worktree); len(changed) > 0 {
		d.Reap, d.Reason = false, "changed before removal: "+strings.Join(changed, "; ")
		return d, fmt.Errorf("refusing to remove %s: %s", worktree, d.Reason)
	}
	deleteBranch := st.deleteBranch
	if deleteBranch {
		// A commit made in the window must keep the branch.
		if n, err := unpushedCount(ctx, st.marker.Primary, st.marker.Branch); err != nil || n != 0 {
			deleteBranch = false
		}
	}
	afterRechecks(worktree)
	// The marker is re-read last, after the slow rechecks above. `wt
	// <branch>` spawns its background sweep before it touches the marker at
	// hand-off, so this sweep may have judged an old last_opened_at; and
	// deleting the marker promotes the worktree to permanent, which must win
	// even mid-reap. Nothing slower than this read sits between it and the
	// removal.
	if reason := reopenedReason(worktree, opts.Grace); reason != "" {
		d.Reap, d.Reason = false, reason
		return d, fmt.Errorf("refusing to remove %s: %s", worktree, d.Reason)
	}
	ro := trash.RemoveOptions{Force: st.hasSubmodules, DeleteBranch: deleteBranch, PR: st.marker.PR}
	if _, err := trash.SoftDeleteWith(ctx, st.ws, st.marker.Primary, ro); err != nil {
		d.Reap, d.Reason = false, err.Error()
		return d, fmt.Errorf("soft-deleting %s: %w", worktree, err)
	}
	return d, nil
}

// reopenedReason re-reads the marker and re-applies the grace against the
// current time. It returns why removal must stop, or "" to proceed.
func reopenedReason(worktree string, grace time.Duration) string {
	m, err := ReadMarker(worktree)
	if err != nil {
		return fmt.Sprintf("no usable %s marker before removal: %v", MarkerName, err)
	}
	if grace > 0 {
		if age := time.Since(openedAt(m)); age < grace {
			return fmt.Sprintf("opened again before removal (%s ago, sweep grace %s)", age.Round(time.Second), grace)
		}
	}
	return ""
}

// recheckClean re-reads the worktree's status, its submodules' status and
// their unpushed commits, returning a reason for each that is dirty or
// unreadable.
func recheckClean(ctx context.Context, worktree string) []string {
	var out []string
	if n, err := dirtyCount(ctx, worktree); err != nil {
		out = append(out, fmt.Sprintf("could not read git status: %v", err))
	} else if n > 0 {
		out = append(out, "has uncommitted changes")
	}
	if dirty, err := submodulesDirty(ctx, worktree); err != nil {
		out = append(out, fmt.Sprintf("could not read submodule status: %v", err))
	} else if dirty {
		out = append(out, "a submodule has uncommitted or untracked changes")
	}
	return append(out, submoduleCommitBlockers(ctx, worktree)...)
}

// submoduleCommitBlockers refuses a worktree whose submodules hold commits
// found on no remote, or whose submodules cannot be checked for them.
func submoduleCommitBlockers(ctx context.Context, worktree string) []string {
	switch unpushed, err := submodulesUnpushed(ctx, worktree); {
	case err != nil:
		return []string{fmt.Sprintf("could not check submodules for unpushed commits: %v", err)}
	case unpushed:
		return []string{"a submodule has commits that exist on no remote"}
	}
	return nil
}

// dirtyCount counts changed paths in the worktree. The error return is not
// incidental: callers must distinguish "clean" from "unknown", and this is
// exactly where discover.FillStatus goes wrong by collapsing both to 0.
//
// --untracked-files=all overrides status.showUntrackedFiles: with that set to
// "no", a plain status omits untracked files and `git worktree remove` then
// deletes them without complaint (verified, git 2.50.1).
func dirtyCount(ctx context.Context, worktree string) (int, error) {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", worktree,
		"status", "--porcelain", "--ignore-submodules=none", "--untracked-files=all").Output()
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

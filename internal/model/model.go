// Package model holds wt's domain types.
package model

import (
	"strings"
	"time"
)

// Kind records who created a worktree.
type Kind string

const (
	KindPrimary       Kind = "primary"        // the repo's own checkout
	KindSibling       Kind = "sibling"        // hand-made, e.g. acme/server-cache
	KindNested        Kind = "nested"         // repo/.worktrees/*
	KindClaudeManaged Kind = "claude-managed" // repo/.claude/worktrees/*
	KindForeign       Kind = "foreign"        // anywhere else, e.g. ~/.sprout
)

// PR is a pull request's state, resolved by branch or by a session's pr-link.
type PR struct {
	Number int
	State  string // OPEN, MERGED, CLOSED
}

// Session is one Claude Code session that ran in a worktree.
type Session struct {
	ID         string
	Title      string
	LastPrompt string
	PRNumber   int
	Branch     string
	Cwd        string
	Mtime      time.Time
	Live       bool
	PID        int

	BytesScanned int // instrumentation: bytes read by the tail reader
}

// Label returns the best human-readable name for this session: its
// AI-generated title, else a trimmed form of the last prompt, else the
// session id. Claude Code does not write an ai-title for every session, so
// Title alone is blank for roughly half of real sessions.
func (s Session) Label() string {
	if s.Title != "" {
		return s.Title
	}
	if s.LastPrompt != "" {
		return truncate(oneLine(s.LastPrompt), 60)
	}
	return s.ID
}

// oneLine collapses any newlines in a prompt to single spaces, so a
// multi-line prompt cannot break a one-line row.
func oneLine(s string) string {
	f := func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}
	return strings.Map(f, s)
}

// truncate cuts s to at most n runes, on a rune boundary (not a byte
// boundary — prompts contain non-ASCII), appending "…" when it actually
// truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// Proc is a non-Claude process running inside a worktree.
type Proc struct {
	PID     int
	Command string
	Elapsed string
	Cwd     string
}

// Workspace is one git worktree with everything known about it.
type Workspace struct {
	Repo       string
	Path       string
	Branch     string
	Head       string
	DirtyCount int
	// StatusKnown reports whether `git status` was successfully read for
	// this workspace (see discover.FillStatus). Its zero value, false, is
	// deliberately "unknown" rather than "known clean": a Workspace that
	// never passed through FillStatus (a bare struct literal in a test, a
	// pre-migration snapshot.json entry missing the field, a worktree where
	// the status call failed) must never be mistaken for a clean one just
	// because DirtyCount also defaults to 0. PruneBlockers treats
	// !StatusKnown as a blocker for exactly this reason -- fail closed, not
	// fail open.
	StatusKnown bool
	// HasUpstream reports whether this workspace's branch has an upstream
	// configured. Its zero value, false, is deliberately "warn" rather than
	// "in sync": git omits the `# branch.ab` line from
	// `git status --porcelain=v2 --branch` output entirely when there is no
	// upstream (see discover.ParseStatusV2), so a Workspace that never
	// passed through a successful FillStatus (a bare struct literal in a
	// test, a pre-migration snapshot.json entry missing the field, a
	// worktree whose status call failed and whose rev-list fallback also
	// couldn't resolve @{u}) must never be mistaken for "pushed and in
	// sync" just because Ahead also defaults to 0. The delete confirmation
	// treats !HasUpstream as "warn: never pushed" for exactly this reason
	// -- fail closed (over-warn), never fail open. Same pattern as
	// StatusKnown above.
	HasUpstream bool
	Ahead       int
	Behind      int
	PR          PR
	Sessions    []Session
	Procs       []Proc
	LastUsed    time.Time
	Kind        Kind

	// AgentStatus is the most attention-worthy state among the herdr agents
	// running in this worktree: "blocked", "working", "idle", "done", or ""
	// when none are. wt cannot compute this itself -- it only sees processes
	// via ps, which cannot say whether an agent is waiting on a human.
	AgentStatus string
	// AgentCount is how many herdr agents are running here.
	AgentCount int
	// AgentProbe records how the herdr lookup went. See AgentProbe.
	AgentProbe AgentProbe
}

// AgentProbe is the outcome of asking herdr what is running in a worktree.
//
// This is the package invariant's three-way split (see discover's package
// comment), and the reason a plain bool would be wrong. Success and
// unreadable are the usual pair, but "absent" here is a genuine, benign
// third answer: wt works perfectly well with no herdr installed, and on such
// a machine every worktree really does have zero agents. Treating that as
// unknown would block deletion of every worktree forever for every user who
// does not run herdr.
//
// So the zero value is Absent, not Unreadable -- a deliberate departure from
// StatusKnown's polarity, justified only because absence is overwhelmingly
// the common case AND is independently verifiable (no socket, no binary).
// AgentProbeUnreadable is reserved for a daemon that is demonstrably up but
// would not answer, which is unknown and does block. A bare Workspace{} is
// still refused by PruneBlockers via !StatusKnown, so nothing rests on this
// field alone.
type AgentProbe uint8

const (
	// AgentProbeAbsent means no herdr daemon is running. A real answer.
	AgentProbeAbsent AgentProbe = iota
	// AgentProbeOK means herdr answered and AgentStatus/AgentCount are real.
	AgentProbeOK
	// AgentProbeUnreadable means herdr is up but could not be read. Unknown.
	AgentProbeUnreadable
)

// HasBusyAgent reports whether a herdr agent is actively working in this
// worktree or blocked waiting on a human. Either way it is in use.
//
// "idle" and "done" deliberately do not count: an agent sitting at a prompt
// is exactly the long-lived session the user keeps around on purpose, and
// refusing to ever delete those would defeat the point of the delete gate.
func (w Workspace) HasBusyAgent() bool {
	return w.AgentProbe == AgentProbeOK &&
		(w.AgentStatus == "working" || w.AgentStatus == "blocked")
}

// HasLiveSession reports whether any session in this workspace is running.
func (w Workspace) HasLiveSession() bool {
	for _, s := range w.Sessions {
		if s.Live {
			return true
		}
	}
	return false
}

// FreeHint is advisory only. It never authorises deletion; see PruneBlockers.
func (w Workspace) FreeHint() bool {
	return w.PR.State == "MERGED" &&
		w.DirtyCount == 0 &&
		!w.HasLiveSession() &&
		len(w.Procs) == 0
}

// PruneBlockers lists reasons this workspace must not be removed. An empty
// slice means removal is permitted, though still only after confirmation.
func (w Workspace) PruneBlockers() []string {
	var b []string
	if w.Kind == KindPrimary {
		b = append(b, "is the repo's primary checkout")
	}
	if !w.StatusKnown {
		b = append(b, "git status could not be read")
	}
	if w.HasBusyAgent() {
		b = append(b, "has a "+w.AgentStatus+" herdr agent")
	}
	if w.AgentProbe == AgentProbeUnreadable {
		b = append(b, "herdr is running but could not be asked what is in this worktree")
	}
	if w.DirtyCount > 0 {
		b = append(b, "has uncommitted changes")
	}
	if w.HasLiveSession() {
		b = append(b, "has a live Claude session")
	}
	if len(w.Procs) > 0 {
		b = append(b, "has running processes")
	}
	return b
}

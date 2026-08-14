// The "d d" delete-confirmation flow opened from the dashboard's normal
// mode (see ui.go's updateNormal). A stray keypress can never destroy
// anything: the first "d" only arms a confirmation popup showing exactly
// what will happen (mode, path, branch, and any PruneBlockers refusal); a
// second "d" within confirmWindow is the only thing that executes it, and
// every other key -- including a "d" that arrives too late -- cancels back
// to the dashboard untouched.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/theczechr/wt/internal/discover"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/trash"
)

// confirmWindow is how long a second "d" is honoured as a confirmation
// after the first one armed it. A "d" that arrives later is treated like
// any other key: it cancels rather than silently re-arming, so an old,
// forgotten arm can never be woken up by an unrelated keypress.
const confirmWindow = 3 * time.Second

// updateConfirmDelete handles the single keystroke the delete-confirmation
// popup waits on.
func (m uiModel) updateConfirmDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if msg.String() == "d" && time.Since(m.confirmArmedAt) <= confirmWindow {
		return m.executeDelete()
	}
	m.picker = pickerNone
	return m, nil
}

// executeDelete fires the injected DeleteFunc for the armed target.
// PruneBlockers is re-checked inside del itself (trash.SoftDelete /
// trash.HardDelete both consult it before touching git) -- this is
// deliberately not re-derived here, so there is exactly one place in the
// whole codebase that decides whether a deletion is allowed.
func (m uiModel) executeDelete() (tea.Model, tea.Cmd) {
	ws := m.confirmTarget
	m.picker = pickerNone
	if m.del == nil {
		return m, nil
	}
	m.deleting = true
	return m, m.deleteCmd(ws)
}

func (m uiModel) deleteCmd(ws model.Workspace) tea.Cmd {
	del := m.del
	return func() tea.Msg {
		entry, info, err := del(ws)
		return deleteDoneMsg{path: ws.Path, entry: entry, info: info, err: err}
	}
}

// unpushedCountCmd fires the one discover.LocalOnlyCommitCount fork for a
// just-armed no-upstream confirmation. Fired exactly once per arm, from
// updateNormal's "d" case -- never from a refresh/Collect path, and never
// for more than the single armed target.
func unpushedCountCmd(path string) tea.Cmd {
	return func() tea.Msg {
		n, ok := discover.LocalOnlyCommitCount(context.Background(), path)
		return unpushedCountMsg{path: path, count: n, ok: ok}
	}
}

// applyUnpushedCount lands a completed unpushedCountCmd result. It is a
// no-op whenever the popup has since moved on -- cancelled back to the
// dashboard, or re-armed on a different target -- so a slow git call from
// an old arm can never overwrite what's currently on screen.
func (m uiModel) applyUnpushedCount(msg unpushedCountMsg) uiModel {
	if m.picker != pickerConfirmDelete || m.confirmTarget.Path != msg.path {
		return m
	}
	m.confirmUnpushedCount = msg.count
	m.confirmUnpushedCountLoaded = msg.ok
	return m
}

// applyDeleteResult handles a completed delete. On success the workspace is
// removed from the in-memory list immediately (so it disappears without a
// manual 'R') and a background refresh is kicked off to reconcile the rest
// of the dashboard (section counts, sort order) exactly like the 'R' key
// does. On failure -- refused by PruneBlockers, or any other error -- the
// list is left untouched and the reason is shown as a status line.
func (m uiModel) applyDeleteResult(msg deleteDoneMsg) (tea.Model, tea.Cmd) {
	m.deleting = false
	if msg.err != nil {
		if be, ok := msg.err.(*trash.BlockedError); ok {
			m.status = "can't delete: " + strings.Join(be.Blockers, "; ")
		} else {
			m.status = "delete failed: " + msg.err.Error()
		}
		return m, nil
	}

	m = m.removeWorkspace(msg.path)
	if msg.entry != nil {
		m.trash = append([]trash.Entry{*msg.entry}, m.trash...)
	}
	switch {
	case msg.info != "":
		m.status = msg.info
	default:
		m.status = "deleted " + msg.path
	}

	if m.refresh != nil {
		m.refreshing = true
		return m, m.refreshCmd()
	}
	return m, nil
}

// removeWorkspace drops the workspace at path from the master list and
// clamps the cursor back into range -- the local, instant half of "the list
// refreshes without a manual R" (the background refresh triggered
// alongside it is the authoritative half, reconciling everything else).
func (m uiModel) removeWorkspace(path string) uiModel {
	out := make([]model.Workspace, 0, len(m.ws))
	for _, w := range m.ws {
		if w.Path != path {
			out = append(out, w)
		}
	}
	m.ws = out
	if n := len(m.visible()); m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m
}

// deleteModeLabel is what the confirmation popup shows for the configured
// mode -- display only; the actual behaviour is entirely owned by del.
func (m uiModel) deleteModeLabel() string {
	if m.trashMode == "hard" {
		return "hard"
	}
	return "soft"
}

// confirmDeleteWidth is the popup's outer width, wide enough for the
// longest row (a blocker line) plus margin, capped to the terminal width.
const confirmDeleteWidth = 62

// viewConfirmDelete renders the armed confirmation popup: mode, target
// worktree, branch and path, then either the PruneBlockers refusal (when
// any) or a short note on what confirming will do, and the key hint.
// Same no-compositing-layer contract as viewWhichKey: this is the whole
// frame while armed, not a floating box over the dashboard.
func (m uiModel) viewConfirmDelete(outerWidth, outerHeight int) string {
	popupWidth := confirmDeleteWidth
	if popupWidth > outerWidth {
		popupWidth = outerWidth
	}
	inner := popupWidth - 2

	ws := m.confirmTarget
	mode := m.deleteModeLabel()
	blockers := ws.PruneBlockers()

	var rows []string
	rows = append(rows, fmt.Sprintf("%s delete", mode))
	rows = append(rows, displayName(ws)+"  ("+ws.Branch+")")
	rows = append(rows, ws.Path)
	rows = append(rows, "")
	switch {
	case len(blockers) > 0:
		rows = append(rows, "blocked:")
		for _, b := range blockers {
			rows = append(rows, "  "+b)
		}
	case mode == "hard":
		rows = append(rows, "worktree removed; branch dropped if merged (git branch -d)")
	default:
		rows = append(rows, "worktree removed; branch kept, recoverable via <space>t")
	}
	if warn := m.unpushedWarning(); warn != "" {
		rows = append(rows, "")
		rows = append(rows, warn)
	}
	if warn := m.unresolvedProcsWarning(); warn != "" {
		rows = append(rows, "")
		rows = append(rows, warn)
	}
	rows = append(rows, "")
	rows = append(rows, "d again to confirm  ·  any other key cancels")

	content := make([]string, len(rows))
	for i, r := range rows {
		role := roleFg
		switch {
		case len(blockers) > 0 && strings.HasPrefix(r, "  "):
			role = roleOrange
		case strings.HasPrefix(r, "⚠"):
			// Deliberately a different colour from roleOrange's blocker
			// rows above -- this is a caution, never a refusal. See
			// unpushedWarning.
			role = roleAmber
		}
		content[i] = styleFor(role, false).Render(padField(r, inner))
	}

	title := "DELETE?"
	if len(blockers) > 0 {
		title = "BLOCKED"
	}
	popup := renderPane(title, content, popupWidth, len(rows)+2)
	return lipgloss.Place(outerWidth, outerHeight, lipgloss.Center, lipgloss.Center, popup)
}

// unpushedWarning returns the delete confirmation's unpushed-work caution
// for the armed target, or "" when there's nothing to caution about.
//
// This is always a warning, never a blocker, and deliberately not folded
// into PruneBlockers: soft delete keeps the branch regardless of push
// state, so these commits stay reachable on disk and Restore rebuilds the
// worktree from them exactly like it does for any other kept branch.
// Blocking on it would refuse a delete that's actually completely safe,
// and -- with roughly 40% of real worktrees having no upstream at all --
// would make deletion nearly unusable.
//
// The no-upstream case leads on HasUpstream rather than Ahead because a
// branch with no upstream reports Ahead=0 too (see
// discover.ParseStatusV2) -- checking Ahead first would silently say
// nothing at all about a branch that has genuinely never been pushed.
func (m uiModel) unpushedWarning() string {
	ws := m.confirmTarget
	switch {
	case !ws.HasUpstream:
		if m.confirmUnpushedCountLoaded {
			return "⚠ never pushed — " + commitsExistOnlyHere(m.confirmUnpushedCount)
		}
		return "⚠ never pushed — commits exist only here"
	case ws.Ahead > 0:
		return "⚠ " + commitsNotPushed(ws.Ahead)
	default:
		return ""
	}
}

// commitsExistOnlyHere renders "N commit(s) exist(s) only here" with
// correct singular/plural subject-verb agreement -- "1 commit exists",
// not "1 commit exist".
func commitsExistOnlyHere(n int) string {
	if n == 1 {
		return "1 commit exists only here"
	}
	return fmt.Sprintf("%d commits exist only here", n)
}

// commitsNotPushed renders "N commit(s) not pushed".
func commitsNotPushed(n int) string {
	if n == 1 {
		return "1 commit not pushed"
	}
	return fmt.Sprintf("%d commits not pushed", n)
}

// unresolvedProcsWarning returns a caution about the last process
// snapshot's unresolved cwds, or "" when every candidate process resolved.
//
// This is global, not scoped to confirmTarget: discover.SnapshotErr's
// UnresolvedCwds counts processes whose cwd probe failed outright (see its
// doc comment), and those processes could belong to ANY worktree, this one
// included -- there is no way to attribute an unresolved cwd to a
// particular workspace, since attributing it is exactly what failed. So
// every armed confirmation shows the same count: it is not "this worktree
// has unchecked processes," it is "the process check that would have
// caught a running process here was incomplete, system-wide, N times."
//
// Always a warning, never a blocker -- same contract as unpushedWarning.
// Blocking every delete on an unresolved lsof probe (routine for
// short-lived or another-user's processes on this machine) would make
// deletion nearly unusable for exactly the reason FillStatus's fix had to
// stay a warning-shaped signal for HasUpstream, not a hard refusal.
func (m uiModel) unresolvedProcsWarning() string {
	switch {
	case m.unresolvedProcs <= 0:
		return ""
	case m.unresolvedProcs == 1:
		return "⚠ 1 process couldn't be checked"
	default:
		return fmt.Sprintf("⚠ %d processes couldn't be checked", m.unresolvedProcs)
	}
}

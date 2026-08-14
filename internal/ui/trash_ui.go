// The trash view opened by "<space> t": lists soft-deleted worktrees and
// lets the user restore ("u") or purge ("D") one. Purging only ever drops
// the manifest record -- the worktree directory itself was already removed
// at soft-delete time -- so nothing here can lose work; see
// trash.PurgeExpired's own comment for the same invariant applied to
// startup expiry.
package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/theczechr/wt/internal/trash"
)

// updateTrash handles keys while the trash view owns input.
func (m uiModel) updateTrash(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.picker = pickerNone
		return m, nil
	}
	if m.trashBusy {
		return m, nil
	}
	switch msg.String() {
	case "j", "down":
		if m.trashCursor < len(m.trash)-1 {
			m.trashCursor++
		}
	case "k", "up":
		if m.trashCursor > 0 {
			m.trashCursor--
		}
	case "u":
		if len(m.trash) == 0 || m.restore == nil {
			return m, nil
		}
		e := m.trash[m.trashCursor]
		m.trashBusy = true
		return m, m.restoreCmd(e)
	case "D":
		if len(m.trash) == 0 || m.purge == nil {
			return m, nil
		}
		e := m.trash[m.trashCursor]
		m.trashBusy = true
		return m, m.purgeCmd(e)
	}
	return m, nil
}

func (m uiModel) restoreCmd(e trash.Entry) tea.Cmd {
	restore := m.restore
	return func() tea.Msg { return restoreDoneMsg{entry: e, err: restore(e)} }
}

func (m uiModel) purgeCmd(e trash.Entry) tea.Cmd {
	purge := m.purge
	return func() tea.Msg { return purgeDoneMsg{entry: e, err: purge(e)} }
}

// removeTrashEntry drops e from the in-memory trash list (matching Path and
// DeletedAt, same identity Remove uses on the manifest) and clamps the
// cursor back into range.
func (m uiModel) removeTrashEntry(e trash.Entry) uiModel {
	out := make([]trash.Entry, 0, len(m.trash))
	for _, x := range m.trash {
		if x.Path == e.Path && x.DeletedAt.Equal(e.DeletedAt) {
			continue
		}
		out = append(out, x)
	}
	m.trash = out
	if m.trashCursor >= len(m.trash) {
		m.trashCursor = len(m.trash) - 1
	}
	if m.trashCursor < 0 {
		m.trashCursor = 0
	}
	return m
}

// applyRestoreResult handles a completed Restore. On success the entry is
// dropped from the trash list and a background refresh is kicked off so
// the recreated worktree appears in the main dashboard without a manual
// 'R'. On failure (most commonly "path already exists") the entry stays in
// the trash list and the reason is shown as a status line.
func (m uiModel) applyRestoreResult(msg restoreDoneMsg) (tea.Model, tea.Cmd) {
	m.trashBusy = false
	if msg.err != nil {
		m.status = "restore failed: " + msg.err.Error()
		return m, nil
	}
	m = m.removeTrashEntry(msg.entry)
	m.status = "restored " + msg.entry.Path
	if m.refresh != nil {
		m.refreshing = true
		return m, m.refreshCmd()
	}
	return m, nil
}

// applyPurgeResult handles a completed manifest-only purge.
func (m uiModel) applyPurgeResult(msg purgeDoneMsg) (tea.Model, tea.Cmd) {
	m.trashBusy = false
	if msg.err != nil {
		m.status = "purge failed: " + msg.err.Error()
		return m, nil
	}
	m = m.removeTrashEntry(msg.entry)
	m.status = "purged " + msg.entry.Path + " from trash"
	return m, nil
}

// buildTrashLines renders the trash list into exact-width screen lines,
// reusing the same cursor-prefix convention as the main list and the find
// picker.
func buildTrashLines(entries []trash.Entry, cursor, width int, now time.Time) []screenLine {
	lines := make([]screenLine, len(entries))
	for i, e := range entries {
		selected := i == cursor
		prefix := "  "
		prefixRole := rolePlain
		if selected {
			prefix = "❯ "
			prefixRole = roleAccentBold
		}
		rowWidth := width - lipgloss.Width(prefix)
		if rowWidth < 0 {
			rowWidth = 0
		}
		text := styleFor(prefixRole, selected).Render(prefix) + renderTrashRow(e, rowWidth, selected, now)
		lines[i] = screenLine{text: text, wsIndex: i}
	}
	return lines
}

const (
	trashRepoWidth = 16
	trashAgeWidth  = 6
)

// renderTrashRow formats one trash entry as an exact-width, styled line:
// repo, branch (gets whatever width is left over), age.
func renderTrashRow(e trash.Entry, width int, selected bool, now time.Time) string {
	if width <= 0 {
		return ""
	}
	repo := padField(e.Repo, trashRepoWidth)
	age := padField(e.Age(now), trashAgeWidth)
	fixed := lipgloss.Width(repo) + lipgloss.Width(age) + 2
	branchBudget := width - fixed
	if branchBudget < 0 {
		branchBudget = 0
	}
	branch := padField(e.Branch, branchBudget)

	var written int
	var out string
	write := func(text string, r role) {
		out += styleFor(r, selected).Render(text)
		written += lipgloss.Width(text)
	}
	write(repo, roleFg)
	write(" ", rolePlain)
	write(branch, roleDim)
	write(" ", rolePlain)
	write(age, roleDim)
	if pad := width - written; pad > 0 {
		out += styleFor(rolePlain, selected).Render(strings.Repeat(" ", pad))
	}
	return out
}

// viewTrashPicker renders the full-screen trash overlay: a titled pane
// listing every trashed entry, scrolled via the same visibleWindow helper
// the main dashboard uses.
func (m uiModel) viewTrashPicker(width, outerHeight int) string {
	inner := width - 2
	if inner < 0 {
		inner = 0
	}
	rows := outerHeight - 2
	if rows < 1 {
		rows = 1
	}

	now := time.Now()
	cursor := m.trashCursor
	if len(m.trash) > 0 && cursor >= len(m.trash) {
		cursor = len(m.trash) - 1
	}
	lines := buildTrashLines(m.trash, cursor, inner, now)
	start, end := visibleWindow(lines, cursor, rows)

	content := make([]string, 0, rows)
	for _, l := range lines[start:end] {
		content = append(content, l.text)
	}
	if len(m.trash) == 0 {
		content = append(content, styleFor(roleDim, false).Render(padField("  (trash is empty)", inner)))
	}

	pane := renderPane("TRASH", content, width, outerHeight)
	footer := styleFor(roleDim, false).Render("j/k move  u restore  D purge  esc back")
	if m.trashBusy {
		footer += "  " + styleFor(roleAccentBold, false).Render("⟳")
	}
	return pane + "\n\n" + footer + "\n"
}

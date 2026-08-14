// Find worktree: a LazyVim-style fuzzy picker over the in-memory workspace
// list opened by "<space> f". It does no I/O -- m.ws is already fully
// collected by the time the dashboard is up, so every keystroke is a pure,
// instant re-filter/re-rank, unlike the grep picker's streaming search.
package ui

import (
	"sort"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/theczechr/wt/internal/model"
)

// findResults fuzzy-matches m.findQuery against every workspace's display
// name, branch and repo, ranked best-match-first. An empty query returns
// the full list in its existing (sortWorkspaces) order.
func (m uiModel) findResults() []model.Workspace {
	if m.findQuery == "" {
		out := make([]model.Workspace, len(m.ws))
		copy(out, m.ws)
		return out
	}
	type scored struct {
		w     model.Workspace
		score int
	}
	var matches []scored
	for _, w := range m.ws {
		hay := displayName(w) + " " + w.Branch + " " + w.Repo
		if score, ok := fuzzyScore(m.findQuery, hay); ok {
			matches = append(matches, scored{w, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	out := make([]model.Workspace, len(matches))
	for i, s := range matches {
		out[i] = s.w
	}
	return out
}

// fuzzyScore reports whether query matches hay as a case-insensitive
// subsequence (fzf/LazyVim style: every query rune must appear in hay, in
// order, though not necessarily contiguously) and, if so, a score where
// higher is a better match: contiguous runs and matches nearer the start of
// hay score higher. This is deliberately simple -- the picker runs over an
// in-memory list of at most a few hundred workspaces, not a
// performance-critical path.
func fuzzyScore(query, hay string) (score int, ok bool) {
	if query == "" {
		return 0, true
	}
	q := []rune(strings.ToLower(query))
	h := []rune(strings.ToLower(hay))
	qi := 0
	lastMatch := -1
	for hi := 0; hi < len(h) && qi < len(q); hi++ {
		if h[hi] != q[qi] {
			continue
		}
		switch {
		case lastMatch == hi-1:
			score += 3 // contiguous run
		case lastMatch == -1 && hi < 5:
			score += 2 // an early first match
		default:
			score++
		}
		lastMatch = hi
		qi++
	}
	return score, qi == len(q)
}

// updateFind handles keys while the find picker owns input. Arrow keys and
// Ctrl-N/Ctrl-P navigate; j/k are deliberately NOT bound here (unlike the
// main list) so they remain literal query text -- this is a text-entry
// picker, and "j"/"k" are common substrings of real branch/repo names.
func (m uiModel) updateFind(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.picker = pickerNone
		m.findQuery = ""
		m.findCursor = 0
		return m, nil
	case tea.KeyEnter:
		results := m.findResults()
		if len(results) > 0 && m.findCursor < len(results) {
			m.chosen = results[m.findCursor].Path
			m.action = ActionCd
			m.picker = pickerNone
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		if n := len(m.findResults()); m.findCursor < n-1 {
			m.findCursor++
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		if m.findCursor > 0 {
			m.findCursor--
		}
		return m, nil
	case tea.KeyBackspace:
		if m.findQuery != "" {
			_, size := utf8.DecodeLastRuneInString(m.findQuery)
			m.findQuery = m.findQuery[:len(m.findQuery)-size]
		}
		m.findCursor = 0
		return m, nil
	case tea.KeySpace:
		m.findQuery += " "
		m.findCursor = 0
		return m, nil
	case tea.KeyRunes:
		m.findQuery += string(msg.Runes)
		m.findCursor = 0
		return m, nil
	}
	return m, nil
}

// buildFindLines renders the find picker's flat, fuzzy-ranked result list
// into exact-width screen lines, reusing RenderRow so a find result shows
// the same status affordances (marker, PR, dirty count, agent/FREE tags) as
// the main dashboard list.
func buildFindLines(results []model.Workspace, cursor, width int) []screenLine {
	lines := make([]screenLine, len(results))
	for i, w := range results {
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
		text := styleFor(prefixRole, selected).Render(prefix) + RenderRow(w, rowWidth, selected)
		lines[i] = screenLine{text: text, wsIndex: i}
	}
	return lines
}

// viewFindPicker renders the full-screen find overlay: a titled pane with
// the query input on its first line and the ranked result list below,
// scrolled to keep the cursor on screen via the same visibleWindow helper
// the dashboard's own worktree pane uses.
func (m uiModel) viewFindPicker(width, outerHeight int) string {
	inner := width - 2
	if inner < 0 {
		inner = 0
	}
	results := m.findResults()
	cursor := m.findCursor
	if len(results) > 0 && cursor >= len(results) {
		cursor = len(results) - 1
	}

	rows := outerHeight - 2 - 1 // borders, then the input line
	if rows < 1 {
		rows = 1
	}
	lines := buildFindLines(results, cursor, inner)
	start, end := visibleWindow(lines, cursor, rows)

	content := make([]string, 0, rows+1)
	content = append(content, renderPickerInput("Find:", m.findQuery, inner))
	if len(results) == 0 {
		content = append(content, styleFor(roleDim, false).Render(padField("  (no matches)", inner)))
	}
	for _, l := range lines[start:end] {
		content = append(content, l.text)
	}

	pane := renderPane("FIND", content, width, outerHeight)
	footer := styleFor(roleDim, false).Render("↑/↓ ^n/^p move  ⏎ cd  esc cancel")
	return pane + "\n\n" + footer + "\n"
}

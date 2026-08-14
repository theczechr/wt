// The "<space>" which-key popup and small helpers shared by the find and
// grep pickers it opens into.
package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// updateWhichKey handles the single keystroke the "⎵" popup waits on. Any
// key other than "f"/"g" -- including Esc -- dismisses it without taking an
// action, matching LazyVim's which-key.
func (m uiModel) updateWhichKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch msg.String() {
	case "f":
		m.picker = pickerFind
		m.findQuery = ""
		m.findCursor = 0
	case "g":
		m.picker = pickerGrep
		m.grep = newGrepState(m.ws)
	case "t":
		m.picker = pickerTrash
		m.trashCursor = 0
	default:
		m.picker = pickerNone
	}
	return m, nil
}

// whichKeyWidth is the popup's outer width -- wide enough for its longest
// row ("  g   grep sessions") plus margin, capped to the terminal width.
const whichKeyWidth = 34

// viewWhichKey renders the which-key popup centred in an outerWidth x
// outerHeight canvas. There is no compositing layer here (see View()'s
// picker comment): the popup is the entire frame, not a floating box drawn
// over the dashboard's own ANSI output.
func (m uiModel) viewWhichKey(outerWidth, outerHeight int) string {
	popupWidth := whichKeyWidth
	if popupWidth > outerWidth {
		popupWidth = outerWidth
	}
	inner := popupWidth - 2
	rows := []string{
		"  f   find worktree",
		"  g   grep sessions",
		"  t   trash",
		"  esc cancel",
	}
	content := make([]string, len(rows))
	for i, r := range rows {
		content[i] = styleFor(roleFg, false).Render(padField(r, inner))
	}
	popup := renderPane("⎵", content, popupWidth, len(rows)+2)
	return lipgloss.Place(outerWidth, outerHeight, lipgloss.Center, lipgloss.Center, popup)
}

// renderPickerInput renders a text-entry picker's single query row --
// "Label: query█" -- as one exactly-width-column styled line. label is a
// short, fixed literal ("Find:", "Grep:") that always fits; query is padded
// or truncated (via padField) to whatever room is left, so the whole line
// still measures exactly width even in a pane too narrow for the label
// itself.
func renderPickerInput(label, query string, width int) string {
	if width <= 0 {
		return ""
	}
	prefix := label + " "
	prefixW := lipgloss.Width(prefix)
	if prefixW >= width {
		return styleFor(roleAccentBold, false).Render(padField(prefix, width))
	}
	body := padField(query+"█", width-prefixW)
	return styleFor(roleAccentBold, false).Render(prefix) + styleFor(roleFg, false).Render(body)
}

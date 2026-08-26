package ui

import (
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theczechr/wt/internal/resolve"
)

// The "new worktree" picker: a single text field for a branch name.
//
// It deliberately does no work itself. Creating a worktree clones a
// submodule and can run a post_create install, which routinely takes
// minutes -- far too long to hold a TUI frame, and its output is exactly
// what a user wants to watch scroll past. So this picker only collects the
// name and quits with ActionNew; main performs the create afterwards, on the
// same path `wt <branch>` already uses, and then hands off the result the
// same way ⏎ does. That keeps one implementation of create, not two.

// updateNew handles keys while the new-worktree picker owns input. As in
// the find picker, j/k stay literal text: they are common substrings of real
// branch names.
func (m uiModel) updateNew(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.picker = pickerNone
		m.newQuery = ""
		m.newErr = ""
		return m, nil
	case tea.KeyEnter:
		branch := m.newQuery
		if branch == "" {
			return m, nil
		}
		// Validated here rather than after the TUI exits, so a typo is
		// correctable in place instead of dumping the user back to a shell
		// with an error and a lost input.
		if err := resolve.ValidBranchName(branch); err != nil {
			m.newErr = err.Error()
			return m, nil
		}
		// repo\tbranch: creating a branch needs a repo to create it in,
		// and the row under the cursor is the least surprising choice.
		m.chosen = m.newRepo() + "\t" + branch
		m.action = ActionNew
		m.picker = pickerNone
		return m, tea.Quit
	case tea.KeyBackspace:
		if m.newQuery != "" {
			_, size := utf8.DecodeLastRuneInString(m.newQuery)
			m.newQuery = m.newQuery[:len(m.newQuery)-size]
		}
		m.newErr = ""
		return m, nil
	case tea.KeySpace:
		m.newQuery += " "
		m.newErr = ""
		return m, nil
	case tea.KeyRunes:
		m.newQuery += string(msg.Runes)
		m.newErr = ""
		return m, nil
	}
	return m, nil
}

func (m uiModel) viewNewPicker(width, outerHeight int) string {
	inner := width - 2
	if inner < 0 {
		inner = 0
	}
	rows := outerHeight - 2 - 1
	if rows < 1 {
		rows = 1
	}
	content := make([]string, 0, rows+1)
	content = append(content, renderPickerInput("New worktree, branch:", m.newQuery, inner))

	msg := "existing branch is opened; a new one is created in " + m.newRepo() + " from its base"
	r := roleDim
	if m.newErr != "" {
		msg, r = m.newErr, roleAmber
	}
	content = append(content, styleFor(r, false).Render(padField("  "+msg, inner)))
	for len(content) < rows+1 {
		content = append(content, styleFor(rolePlain, false).Render(padField("", inner)))
	}
	return renderPane("NEW", content, width, outerHeight)
}

// newRepo is the repo a new branch is created in: the one under the worktree
// cursor. A dashboard row is always in some repo, so this is unambiguous in
// the normal case; an empty list falls back to empty, which main reports as
// an unconfigured repo rather than guessing.
func (m uiModel) newRepo() string {
	vis := m.visible()
	if len(vis) == 0 || m.cursor >= len(vis) {
		return ""
	}
	return vis[m.cursor].Repo
}

// A one-shot chooser for the case where a branch name matches more than one
// repository. It is a separate bubbletea program rather than a mode of the
// dashboard: it runs before any dashboard exists, and shares only styling.
package ui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

type chooseModel struct {
	title   string
	options []string
	cursor  int
	chosen  int
	done    bool
	width   int
	height  int
}

func newChooseModel(title string, options []string) chooseModel {
	return chooseModel{title: title, options: options, chosen: -1}
}

func (m chooseModel) Init() tea.Cmd { return nil }

// update holds the whole key contract, separated from Update so it can be
// driven directly by tests without a running program.
func (m chooseModel) update(msg tea.KeyMsg) (chooseModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc", "q":
		m.chosen, m.done = -1, true
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.options)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.chosen, m.done = m.cursor, true
		return m, tea.Quit
	}
	return m, nil
}

func (m chooseModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		next, cmd := m.update(msg)
		return next, cmd
	}
	return m, nil
}

func (m chooseModel) viewAt(width, height int) string {
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	lines := make([]string, 0, len(m.options)+1)
	for i, o := range m.options {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		lines = append(lines, styleFor(roleFg, i == m.cursor).Render(padField(prefix+o, inner)))
	}
	lines = append(lines, styleFor(roleFg, false).Render(padField("  j/k move  ⏎ open  esc cancel", inner)))
	return renderPane(m.title, lines, width, len(lines)+2)
}

func (m chooseModel) View() string {
	width := m.width
	if width <= 0 {
		width = 60
	}
	return m.viewAt(width, m.height)
}

// Interactive reports whether Choose can actually run: a chooser that
// prompted down a pipe would hang a script with no visible reason.
//
// It checks the two streams Choose uses, and stdout is not one of them.
// Choose reads keys from stdin and renders to stderr (bubbletea is given
// tea.WithOutput(os.Stderr) below, because stdout carries wt's machine-read
// output). A non-TTY stdin is the one that hangs: bubbletea waits for a
// keypress that a pipe or /dev/null will never deliver. Checking stdout
// instead had it exactly backwards -- `wt <branch> | cat` refused to prompt
// from a perfectly usable terminal, while `wt <branch> < /dev/null` in a
// terminal went ahead and hung. stderr is required too, since a chooser
// whose frames are being redirected to a file is not one anybody can see.
func Interactive() bool { return interactive(os.Stdin, os.Stderr) }

// interactive is Interactive with the streams injected, so a test can check
// the contract against real pipes instead of the process's own terminal.
func interactive(streams ...*os.File) bool {
	for _, f := range streams {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// Choose presents options and returns the index selected, or -1 if the user
// cancelled.
func Choose(title string, options []string) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("nothing to choose from")
	}
	p := tea.NewProgram(newChooseModel(title, options), tea.WithOutput(os.Stderr))
	out, err := p.Run()
	if err != nil {
		return -1, err
	}
	m, ok := out.(chooseModel)
	if !ok {
		return -1, fmt.Errorf("unexpected model type %T", out)
	}
	return m.chosen, nil
}

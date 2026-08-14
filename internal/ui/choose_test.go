package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestInteractiveChecksTheStreamsChooseActuallyUses pins which streams gate
// the chooser. Choose reads keys from stdin and renders to stderr; stdout is
// not involved at all, so checking it had the contract backwards in both
// directions -- `wt <branch> | cat` refused to prompt from a usable terminal,
// while `wt <branch> < /dev/null` in a terminal went ahead and hung
// bubbletea waiting for a keypress nothing would ever send.
//
// A pipe stands in for the non-terminal case and /dev/null for the terminal
// one: os.ModeCharDevice, the check itself, is true for /dev/null and false
// for a pipe. The mixed cases are the ones that carry the weight -- each
// fails if the corresponding stream stops being checked.
func TestInteractiveChecksTheStreamsChooseActuallyUses(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })

	chr, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chr.Close() })

	for _, tc := range []struct {
		name    string
		streams []*os.File
		want    bool
	}{
		{"both character devices", []*os.File{chr, chr}, true},
		{"stdin is a pipe", []*os.File{pr, chr}, false},
		{"stderr is a pipe", []*os.File{chr, pw}, false},
		{"neither", []*os.File{pr, pw}, false},
	} {
		if got := interactive(tc.streams...); got != tc.want {
			t.Errorf("%s: interactive = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestChooseModelMovesAndSelects(t *testing.T) {
	m := newChooseModel("Which repo?", []string{"backend", "frontend", "dashboard"})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want 2", m.cursor)
	}
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.chosen != 2 {
		t.Errorf("chosen = %d, want 2", m.chosen)
	}
}

func TestChooseModelClampsAtTheEnds(t *testing.T) {
	m := newChooseModel("t", []string{"a", "b"})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 at the top", m.cursor)
	}
	for i := 0; i < 5; i++ {
		m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1 at the bottom", m.cursor)
	}
}

func TestChooseModelEscCancels(t *testing.T) {
	m := newChooseModel("t", []string{"a", "b"})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.chosen != -1 {
		t.Errorf("chosen = %d, want -1 for a cancel", m.chosen)
	}
	if !m.done {
		t.Error("esc must end the program")
	}
}

func TestChooseModelViewShowsEveryOption(t *testing.T) {
	m := newChooseModel("Which repo?", []string{"backend", "frontend"})
	out := m.viewAt(60, 10)
	for _, want := range []string{"backend", "frontend"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q:\n%s", want, out)
		}
	}
}

package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeInto(m uiModel, text string) uiModel {
	for _, r := range text {
		next, _ := m.updateNew(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(uiModel)
	}
	return m
}

// TestNewPickerCarriesABranchNameNotAPath is the contract that makes
// ActionNew different from ActionCd: the worktree does not exist yet, so
// chosen holds a branch name and main is responsible for creating it.
func TestNewPickerCarriesABranchNameNotAPath(t *testing.T) {
	m := withSessions(1)
	m = press(m, "n")
	if m.picker != pickerNew {
		t.Fatal("n did not open the new-worktree picker")
	}
	m = typeInto(m, "feature/thing")
	next, _ := m.updateNew(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(uiModel)

	if m.action != ActionNew {
		t.Fatalf("action = %v, want ActionNew", m.action)
	}
	if m.chosen != "feature/thing" {
		t.Errorf("chosen = %q, want the branch name", m.chosen)
	}
}

// TestNewPickerRejectsABadNameInPlace matters because the alternative is
// dumping the user back to a shell with an error and a lost input. The
// picker stays open and says what is wrong.
func TestNewPickerRejectsABadNameInPlace(t *testing.T) {
	m := press(withSessions(1), "n")
	m = typeInto(m, "..")
	next, _ := m.updateNew(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(uiModel)

	if m.action == ActionNew {
		t.Fatal("an invalid branch name was accepted")
	}
	if m.picker != pickerNew {
		t.Error("picker closed on an invalid name; the typed input is lost")
	}
	if m.newErr == "" {
		t.Error("no reason shown for the rejection")
	}
	if !strings.Contains(m.viewNewPicker(120, 20), m.newErr) {
		t.Error("the rejection reason is not rendered")
	}
}

// TestNewPickerEmptyEnterDoesNothing: enter on an empty field must not
// start a create with no name.
func TestNewPickerEmptyEnterDoesNothing(t *testing.T) {
	m := press(withSessions(1), "n")
	next, _ := m.updateNew(tea.KeyMsg{Type: tea.KeyEnter})
	if next.(uiModel).action == ActionNew {
		t.Error("empty input started a create")
	}
}

// TestNewPickerEscRestoresTheDashboard keeps Esc consistent with every
// other picker, and clears the half-typed name so reopening starts fresh.
func TestNewPickerEscRestoresTheDashboard(t *testing.T) {
	m := typeInto(press(withSessions(1), "n"), "half-typed")
	next, _ := m.updateNew(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(uiModel)
	if m.picker != pickerNone {
		t.Error("esc did not close the picker")
	}
	if m.newQuery != "" {
		t.Errorf("newQuery = %q, want cleared", m.newQuery)
	}
}

// TestNewPickerKeepsJAndKAsText: j and k are common in branch names, so the
// picker must not steal them for navigation the way the main list does.
func TestNewPickerKeepsJAndKAsText(t *testing.T) {
	m := typeInto(press(withSessions(1), "n"), "fix/jk-parser")
	if m.newQuery != "fix/jk-parser" {
		t.Errorf("newQuery = %q, want fix/jk-parser", m.newQuery)
	}
}

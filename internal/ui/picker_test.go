package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theczechr/wt/internal/model"
)

// TestSpaceIsInertWhileFiltering is the explicit regression test the spec
// calls for: a space is legitimate filter query text (e.g. a branch or repo
// name containing a space in a commit message search), so it must never
// open the which-key popup while "/" filtering owns input.
func TestSpaceIsInertWhileFiltering(t *testing.T) {
	m := uiModel{filtering: true, filter: "pr"}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil {
		t.Error("space while filtering must not return a command")
	}
	nm := next.(uiModel)
	if nm.picker != pickerNone {
		t.Errorf("space while filtering must not open the which-key popup, got picker=%v", nm.picker)
	}
	if want := "pr "; nm.filter != want {
		t.Errorf("space while filtering: filter = %q, want %q (appended literally)", nm.filter, want)
	}
	if !nm.filtering {
		t.Error("space while filtering must not exit filter mode")
	}
}

// TestFindAndGrepKeysAreInertWhileFiltering covers "f" and "g" too: they
// must remain literal filter text and must never open a picker while "/"
// owns input, exactly like every other printable key.
func TestFindAndGrepKeysAreInertWhileFiltering(t *testing.T) {
	for _, key := range []string{"f", "g"} {
		m := uiModel{filtering: true, filter: "ser"}
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if cmd != nil {
			t.Errorf("typing %q while filtering must not return a command", key)
		}
		nm := next.(uiModel)
		if nm.picker != pickerNone {
			t.Errorf("typing %q while filtering must not open a picker, got picker=%v", key, nm.picker)
		}
		if want := "ser" + key; nm.filter != want {
			t.Errorf("typing %q while filtering: filter = %q, want %q", key, nm.filter, want)
		}
	}
}

// TestSpaceOpensWhichKeyFromNormalMode is the mirror check: outside
// filtering, space must open the popup.
func TestSpaceOpensWhichKeyFromNormalMode(t *testing.T) {
	m := uiModel{}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if got := next.(uiModel).picker; got != pickerWhichKey {
		t.Errorf("space from normal mode: picker = %v, want pickerWhichKey", got)
	}
}

// TestWhichKeyRoutesFAndGAndDismissesOnAnythingElse covers the popup's own
// contract: "f" opens find, "g" opens grep, and every other key (including
// Esc) dismisses it back to the dashboard without taking an action --
// LazyVim's which-key behaviour.
func TestWhichKeyRoutesFAndGAndDismissesOnAnythingElse(t *testing.T) {
	m := uiModel{picker: pickerWhichKey}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if got := next.(uiModel).picker; got != pickerFind {
		t.Errorf(`"f": picker = %v, want pickerFind`, got)
	}

	m = uiModel{picker: pickerWhichKey}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if got := next.(uiModel).picker; got != pickerGrep {
		t.Errorf(`"g": picker = %v, want pickerGrep`, got)
	}

	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("x")},
		{Type: tea.KeyRunes, Runes: []rune("q")},
	} {
		m = uiModel{picker: pickerWhichKey}
		next, _ = m.Update(msg)
		if got := next.(uiModel).picker; got != pickerNone {
			t.Errorf("%v: picker = %v, want pickerNone (dismissed)", msg, got)
		}
	}
}

// TestWhichKeyCtrlCQuits asserts the popup doesn't swallow the app's own
// quit key.
func TestWhichKeyCtrlCQuits(t *testing.T) {
	m := uiModel{picker: pickerWhichKey}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected the command to produce tea.QuitMsg")
	}
}

func findFixture() []model.Workspace {
	return []model.Workspace{
		{Repo: "backend", Path: "/u/server", Branch: "main"},
		{Repo: "backend", Path: "/u/backend-cache", Branch: "feature/cache-thing"},
		{Repo: "frontend", Path: "/u/web", Branch: "develop"},
	}
}

// TestFindEnterSelectsCd asserts Enter on a find result behaves exactly
// like Enter on the main list: cd to it, no session.
func TestFindEnterSelectsCd(t *testing.T) {
	m := uiModel{ws: findFixture(), picker: pickerFind, findQuery: "cache"}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected the command to produce tea.QuitMsg")
	}
	nm := next.(uiModel)
	if nm.chosen != "/u/backend-cache" {
		t.Errorf("chosen = %q, want /u/backend-cache", nm.chosen)
	}
	if nm.action != ActionCd {
		t.Errorf("action = %v, want ActionCd", nm.action)
	}
}

// TestFindEscReturnsToDashboard asserts Esc leaves the dashboard's own
// state (ws, cursor, filter, sort order) completely untouched -- pickers
// are additive overlays, not destructive ones.
func TestFindEscReturnsToDashboard(t *testing.T) {
	ws := findFixture()
	m := uiModel{ws: ws, cursor: 2, picker: pickerFind, findQuery: "cache"}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("Esc must not return a command")
	}
	nm := next.(uiModel)
	if nm.picker != pickerNone {
		t.Error("Esc must return to the dashboard")
	}
	if nm.cursor != 2 {
		t.Errorf("Esc must not disturb the main list's own cursor, got %d", nm.cursor)
	}
	if len(nm.ws) != len(ws) {
		t.Error("Esc must not disturb the main list's workspace slice")
	}
}

// TestFindNavigationUsesArrowsAndCtrlNP asserts Down/Up and Ctrl-N/Ctrl-P
// move the find cursor, and that "j"/"k" -- bound to navigation on the main
// list -- are instead literal query text inside this text-entry picker.
func TestFindNavigationUsesArrowsAndCtrlNP(t *testing.T) {
	ws := findFixture()

	m := uiModel{ws: ws, picker: pickerFind}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	nm := next.(uiModel)
	if nm.findQuery != "j" {
		t.Errorf(`"j" must be literal query text, got query=%q`, nm.findQuery)
	}
	if nm.findCursor != 0 {
		t.Errorf("typing must not move the cursor, got %d", nm.findCursor)
	}

	m = uiModel{ws: ws, picker: pickerFind}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := next.(uiModel).findCursor; got != 1 {
		t.Fatalf("down arrow: cursor = %d, want 1", got)
	}
	next, _ = next.(uiModel).Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if got := next.(uiModel).findCursor; got != 2 {
		t.Fatalf("ctrl+n: cursor = %d, want 2", got)
	}
	next, _ = next.(uiModel).Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if got := next.(uiModel).findCursor; got != 1 {
		t.Fatalf("ctrl+p: cursor = %d, want 1", got)
	}
	next, _ = next.(uiModel).Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := next.(uiModel).findCursor; got != 0 {
		t.Fatalf("up arrow: cursor = %d, want 0", got)
	}
}

// TestFindBackspaceRemovesOneRuneAndResetsCursor covers UTF-8 correctness
// (a multi-byte rune must be removed whole, not byte-by-byte) alongside the
// cursor reset every query edit performs.
func TestFindBackspaceRemovesOneRuneAndResetsCursor(t *testing.T) {
	m := uiModel{ws: findFixture(), picker: pickerFind, findQuery: "cache→", findCursor: 1}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	nm := next.(uiModel)
	if nm.findQuery != "cache" {
		t.Errorf("backspace: query = %q, want %q (the multi-byte arrow removed whole)", nm.findQuery, "cache")
	}
	if nm.findCursor != 0 {
		t.Errorf("backspace must reset the cursor, got %d", nm.findCursor)
	}
}

// TestFuzzyScoreSubsequenceAndRanking covers the fuzzy matcher's two
// contracts: non-subsequences never match, and a tighter/earlier match
// ranks above a scattered one.
func TestFuzzyScoreSubsequenceAndRanking(t *testing.T) {
	if _, ok := fuzzyScore("xyz", "abcdef"); ok {
		t.Error("a non-subsequence must not match")
	}
	if _, ok := fuzzyScore("", "anything"); !ok {
		t.Error("an empty query must match everything")
	}

	contig, ok := fuzzyScore("srv", "server")
	if !ok {
		t.Fatal("expected \"srv\" to fuzzy-match \"server\"")
	}
	scattered, ok := fuzzyScore("srv", "some random value")
	if !ok {
		t.Fatal("expected \"srv\" to fuzzy-match \"some random value\"")
	}
	if contig <= scattered {
		t.Errorf("a contiguous match must outscore a scattered one: contig=%d scattered=%d", contig, scattered)
	}
}

// TestFindResultsRanksBestMatchFirst is an end-to-end check of the picker's
// own ranking through m.findResults(), not just the raw scorer.
func TestFindResultsRanksBestMatchFirst(t *testing.T) {
	ws := []model.Workspace{
		{Repo: "backend", Path: "/u/gizmo-thing", Branch: "chore/misc"},  // "backend" only fuzzy-matches scattered across repo+branch, weakly
		{Repo: "backend", Path: "/u/backend-cache", Branch: "feature/x"}, // "backend" matches contiguously in the path
	}
	m := uiModel{ws: ws, findQuery: "backend"}
	got := m.findResults()
	if len(got) != 2 {
		t.Fatalf("expected both workspaces to fuzzy-match, got %d", len(got))
	}
	if got[0].Path != "/u/backend-cache" {
		t.Errorf("best match first: got %q first, want /u/backend-cache", got[0].Path)
	}
}

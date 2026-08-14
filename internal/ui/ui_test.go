package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theczechr/wt/internal/model"
)

// TestCtrlCQuitsWhileFiltering asserts that Ctrl-C exits the program even
// while the "/" filter is open. bubbletea does not quit on Ctrl-C by
// default -- without an explicit case for it, updateFiltering treats it as
// an unrecognised keystroke and the filter just swallows it, leaving Esc as
// the only way out.
func TestCtrlCQuitsWhileFiltering(t *testing.T) {
	m := uiModel{filtering: true, filter: "some query"}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a non-nil tea.Cmd (tea.Quit) when Ctrl-C is pressed while filtering")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected the command to produce tea.QuitMsg, got %T", cmd())
	}
}

// TestCtrlCQuitsWhenNotFiltering is the sanity check alongside the test
// above: the existing normal-mode "q"/"ctrl+c" handling in updateNormal
// must be untouched by the filtering fix.
func TestCtrlCQuitsWhenNotFiltering(t *testing.T) {
	m := uiModel{filtering: false}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a non-nil tea.Cmd (tea.Quit) when Ctrl-C is pressed outside the filter")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected the command to produce tea.QuitMsg, got %T", cmd())
	}
}

// TestInitKicksBackgroundRefreshOnlyWhenAlreadyMarkedRefreshing covers the
// snapshot-paint path: Run sets refreshing=true up front exactly when the
// initial data came from a persisted snapshot, and Init must turn that into
// a real background Collect. When refreshing is false (a fresh Collect
// already painted, or refresh is nil), Init must not fire anything -- the
// first run / no-snapshot fallback must not double-collect.
func TestInitKicksBackgroundRefreshOnlyWhenAlreadyMarkedRefreshing(t *testing.T) {
	called := false
	refresh := func() ([]model.Workspace, int) {
		called = true
		return []model.Workspace{{Path: "/fresh"}}, 0
	}

	m := uiModel{refreshing: true, refresh: refresh}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init must return a command when refreshing is true and refresh is set")
	}
	msg := cmd()
	if !called {
		t.Error("Init's command must invoke refresh")
	}
	rm, ok := msg.(refreshedMsg)
	if !ok {
		t.Fatalf("Init's command must produce a refreshedMsg, got %T", msg)
	}
	if len(rm.ws) != 1 || rm.ws[0].Path != "/fresh" {
		t.Errorf("refreshedMsg carried %+v, want the refresh() result", rm.ws)
	}

	notRefreshing := uiModel{refreshing: false, refresh: refresh}
	if cmd := notRefreshing.Init(); cmd != nil {
		t.Error("Init must be a no-op when refreshing is false (fresh Collect already painted)")
	}

	noRefreshFunc := uiModel{refreshing: true, refresh: nil}
	if cmd := noRefreshFunc.Init(); cmd != nil {
		t.Error("Init must be a no-op when refresh is nil, regardless of refreshing")
	}
}

// TestRKeyTriggersRefreshOnceThenGatesWhileInFlight asserts 'R' starts a
// refresh when idle, and is a no-op while one is already running -- so
// mashing R can't stack up concurrent Collects.
func TestRKeyTriggersRefreshOnceThenGatesWhileInFlight(t *testing.T) {
	calls := 0
	refresh := func() ([]model.Workspace, int) {
		calls++
		return nil, 0
	}
	m := uiModel{refresh: refresh}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	nm := next.(uiModel)
	if !nm.refreshing {
		t.Fatal("pressing R must set refreshing")
	}
	if cmd == nil {
		t.Fatal("pressing R must return a non-nil command")
	}
	cmd() // drive it, proving refresh() actually gets called
	if calls != 1 {
		t.Errorf("refresh called %d times, want 1", calls)
	}

	// Now already refreshing: a second R must be a no-op.
	again, cmd2 := nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	if cmd2 != nil {
		t.Error("R while already refreshing must return a nil command")
	}
	if !again.(uiModel).refreshing {
		t.Error("R while already refreshing must leave refreshing true")
	}
	if calls != 1 {
		t.Errorf("refresh must not be called again while one is in flight, got %d calls", calls)
	}
}

// TestApplyRefreshKeepsCursorOnSameWorkspaceByPath is OPT3's core
// correctness requirement: the refreshed set can reorder, gain, or drop
// entries, so the cursor must follow the selected workspace's PATH, not its
// index, or the selection jumps under the user.
func TestApplyRefreshKeepsCursorOnSameWorkspaceByPath(t *testing.T) {
	m := uiModel{
		ws: []model.Workspace{
			{Repo: "a", Path: "/a1"},
			{Repo: "a", Path: "/a2"},
			{Repo: "a", Path: "/a3"},
		},
		cursor:     1, // sits on /a2
		refreshing: true,
	}
	if got := m.visible()[m.cursor].Path; got != "/a2" {
		t.Fatalf("setup: cursor must start on /a2, got %q", got)
	}

	// Refreshed set: /a2 moved to the front, and a brand-new /a0 shows up
	// ahead of it (sorted by path within the same repo/kind).
	refreshed := []model.Workspace{
		{Repo: "a", Path: "/a0"},
		{Repo: "a", Path: "/a2"},
		{Repo: "a", Path: "/a3"},
	}
	next, cmd := m.Update(refreshedMsg{ws: refreshed})
	if cmd != nil {
		t.Error("applying a refreshedMsg must not itself return a command")
	}
	nm := next.(uiModel)
	if nm.refreshing {
		t.Error("refreshing must clear once the refresh lands")
	}
	got := nm.visible()[nm.cursor]
	if got.Path != "/a2" {
		t.Errorf("cursor landed on %q, want it to have followed /a2", got.Path)
	}
}

// threeRepoWorkspaces builds a fixture with three repo sections in sorted
// order -- dashboard (2 worktrees), server (3), web (1) -- so section
// starts land at vis indices 0, 2, 5.
func threeRepoWorkspaces() []model.Workspace {
	return []model.Workspace{
		{Repo: "dashboard", Path: "/ap1"},
		{Repo: "dashboard", Path: "/ap2"},
		{Repo: "backend", Path: "/s1"},
		{Repo: "backend", Path: "/s2"},
		{Repo: "backend", Path: "/s3"},
		{Repo: "frontend", Path: "/w1"},
	}
}

// TestSectionBracketKeysStepAndWrap covers "]"/"[" landing on each section's
// first worktree and wrapping around at both ends.
func TestSectionBracketKeysStepAndWrap(t *testing.T) {
	ws := threeRepoWorkspaces()

	m := uiModel{ws: ws, cursor: 0} // dashboard, /ap1
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if got := next.(uiModel).cursor; got != 2 {
		t.Fatalf("] from section 1 = cursor %d, want 2 (server's first worktree)", got)
	}

	m = next.(uiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if got := next.(uiModel).cursor; got != 5 {
		t.Fatalf("] from section 2 = cursor %d, want 5 (web's first worktree)", got)
	}

	m = next.(uiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if got := next.(uiModel).cursor; got != 0 {
		t.Fatalf("] from the last section = cursor %d, want 0 (wrap to dashboard)", got)
	}

	// Now walk backwards with "[" from mid-section (cursor 3, inside server).
	m = uiModel{ws: ws, cursor: 3}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	if got := next.(uiModel).cursor; got != 0 {
		t.Fatalf("[ from mid-server = cursor %d, want 0 (dashboard's first worktree)", got)
	}

	m = next.(uiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	if got := next.(uiModel).cursor; got != 5 {
		t.Fatalf("[ from the first section = cursor %d, want 5 (wrap to web)", got)
	}
}

// TestSectionBracketKeysNoopWithOneSection asserts "]" and "[" leave the
// cursor exactly where it was -- not even snapped to the section's own top
// row -- when only one repo section is visible.
func TestSectionBracketKeysNoopWithOneSection(t *testing.T) {
	ws := []model.Workspace{
		{Repo: "backend", Path: "/s1"},
		{Repo: "backend", Path: "/s2"},
		{Repo: "backend", Path: "/s3"},
	}
	for _, key := range []string{"]", "["} {
		m := uiModel{ws: ws, cursor: 1}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if got := next.(uiModel).cursor; got != 1 {
			t.Errorf("%q with one section = cursor %d, want 1 (unchanged)", key, got)
		}
	}
}

// TestDigitKeysJumpToNthVisibleSection covers 1-9 jumping straight to a
// section's first worktree, and being a no-op when there's no Nth section.
func TestDigitKeysJumpToNthVisibleSection(t *testing.T) {
	ws := threeRepoWorkspaces()

	m := uiModel{ws: ws, cursor: 4} // mid-server
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if got := next.(uiModel).cursor; got != 5 {
		t.Fatalf("digit 3 = cursor %d, want 5 (web, the 3rd section)", got)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if got := next.(uiModel).cursor; got != 0 {
		t.Fatalf("digit 1 = cursor %d, want 0 (dashboard, the 1st section)", got)
	}

	// Only 3 sections exist -- digit 9 must be a no-op.
	m = uiModel{ws: ws, cursor: 4}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9")})
	if got := next.(uiModel).cursor; got != 4 {
		t.Errorf("digit 9 with only 3 sections = cursor %d, want 4 (unchanged)", got)
	}
}

// TestFilteringCapturesDigitAndBracketKeysAsLiteralText is the trap this
// feature was built next to: '[', ']' and '1'-'9' are all legitimate
// characters to type into a filter (real branch names here contain digits,
// e.g. "pr198", "release/2026-04-14"). While filtering, they must be
// appended to the filter string exactly like any other character and must
// NOT trigger a section jump -- proven here by starting the cursor mid a
// later section (where a real section jump for these keys would land it
// somewhere other than filtering's own top-of-list reset) and asserting the
// only cursor movement observed is that ordinary reset.
func TestFilteringCapturesDigitAndBracketKeysAsLiteralText(t *testing.T) {
	ws := threeRepoWorkspaces() // section starts at 0, 2, 5

	for _, key := range []string{"1", "2", "9", "[", "]"} {
		m := uiModel{ws: ws, filtering: true, filter: "pr", cursor: 4}
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if cmd != nil {
			t.Errorf("typing %q while filtering must not return a command", key)
		}
		nm := next.(uiModel)
		if want := "pr" + key; nm.filter != want {
			t.Errorf("typing %q while filtering: filter = %q, want %q (appended literally)", key, nm.filter, want)
		}
		if !nm.filtering {
			t.Errorf("typing %q while filtering must not exit filter mode", key)
		}
		// updateFiltering always resets the cursor to 0 on every keystroke
		// (see its final line) -- the point here is that it's *that* reset,
		// not a section jump, that moved it: for section 2 (index 2), key
		// "2" would land the cursor on index 2 if mistakenly treated as a
		// jump instead of a filter character.
		if nm.cursor != 0 {
			t.Errorf("typing %q while filtering: cursor = %d, want 0 (filtering's own reset, not a section jump)", key, nm.cursor)
		}
	}
}

// TestApplyRefreshFallsBackToZeroWhenSelectionDisappears covers the case
// where the previously-selected workspace is gone from the refreshed set
// (e.g. its worktree was removed by something else entirely) -- the cursor
// must land somewhere valid (index 0) rather than out of range or on a
// leftover stale entry.
func TestApplyRefreshFallsBackToZeroWhenSelectionDisappears(t *testing.T) {
	m := uiModel{
		ws:     []model.Workspace{{Repo: "a", Path: "/a1"}, {Repo: "a", Path: "/a2"}},
		cursor: 1, // /a2
	}
	refreshed := []model.Workspace{{Repo: "a", Path: "/a1"}} // /a2 is gone
	next, _ := m.Update(refreshedMsg{ws: refreshed})
	nm := next.(uiModel)
	if nm.cursor != 0 {
		t.Errorf("cursor = %d, want 0 when the selected workspace vanished", nm.cursor)
	}
}

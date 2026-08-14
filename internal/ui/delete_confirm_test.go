package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/trash"
)

func deleteFixture() []model.Workspace {
	return []model.Workspace{
		{Repo: "backend", Path: "/u/server-old", Branch: "fix/whatever", Kind: model.KindSibling},
		{Repo: "backend", Path: "/u/server-dirty", Branch: "feature/dirty", Kind: model.KindSibling, DirtyCount: 3},
	}
}

// TestDIsInertWhileFiltering is the explicit regression test the spec
// calls for: "d" is legitimate filter query text (branch/repo names
// containing "d" are common), so it must never arm a delete while "/"
// filtering owns input.
func TestDIsInertWhileFiltering(t *testing.T) {
	m := uiModel{filtering: true, filter: "backend"}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd != nil {
		t.Error("typing d while filtering must not return a command")
	}
	nm := next.(uiModel)
	if nm.picker != pickerNone {
		t.Errorf("typing d while filtering must not arm a delete, got picker=%v", nm.picker)
	}
	if want := "backendd"; nm.filter != want {
		t.Errorf("typing d while filtering: filter = %q, want %q (appended literally)", nm.filter, want)
	}
}

// TestDIsInertInFindPickerTextInput covers the other named case: "d" typed
// into the find picker's query box is literal text, not a delete-arm key.
func TestDIsInertInFindPickerTextInput(t *testing.T) {
	m := uiModel{ws: deleteFixture(), picker: pickerFind, findQuery: "backend"}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	nm := next.(uiModel)
	if nm.picker != pickerFind {
		t.Errorf("typing d in the find picker must not arm a delete, got picker=%v", nm.picker)
	}
	if nm.findQuery != "backendd" {
		t.Errorf("findQuery = %q, want %q", nm.findQuery, "backendd")
	}
}

// TestDIsInertInGrepPickerTextInput mirrors the find-picker case for grep.
func TestDIsInertInGrepPickerTextInput(t *testing.T) {
	m := uiModel{ws: deleteFixture(), picker: pickerGrep, grep: grepState{query: "pay"}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	nm := next.(uiModel)
	if nm.picker != pickerGrep {
		t.Errorf("typing d in the grep picker must not arm a delete, got picker=%v", nm.picker)
	}
	if nm.grep.query != "payd" {
		t.Errorf("grep query = %q, want %q", nm.grep.query, "payd")
	}
}

// TestDArmsConfirmationFromNormalMode asserts a single "d" from the
// dashboard only arms the popup -- it must never itself delete anything.
// deleteFixture()[0] has no upstream (the zero value), so arming it does
// return a command, but it must be the lazy unpushed-commit-count lookup,
// never anything that touches the worktree.
func TestDArmsConfirmationFromNormalMode(t *testing.T) {
	m := uiModel{ws: deleteFixture()}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd == nil {
		t.Fatal("expected the lazy unpushed-count command for a no-upstream target")
	}
	msg := cmd()
	uc, ok := msg.(unpushedCountMsg)
	if !ok {
		t.Fatalf("expected unpushedCountMsg, got %T -- arming must never itself return a delete command", msg)
	}
	if uc.path != "/u/server-old" {
		t.Errorf("unpushedCountMsg.path = %q, want the cursor row", uc.path)
	}
	nm := next.(uiModel)
	if nm.picker != pickerConfirmDelete {
		t.Fatalf("picker = %v, want pickerConfirmDelete", nm.picker)
	}
	if nm.confirmTarget.Path != "/u/server-old" {
		t.Errorf("confirmTarget.Path = %q, want the cursor row", nm.confirmTarget.Path)
	}
}

// TestDArmsConfirmationWithUpstreamFiresNoCommand covers the other half:
// a target that already has an upstream configured needs no extra git
// call -- Ahead is already known -- so arming it must return no command at
// all.
func TestDArmsConfirmationWithUpstreamFiresNoCommand(t *testing.T) {
	ws := deleteFixture()
	ws[0].HasUpstream = true
	m := uiModel{ws: ws}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd != nil {
		t.Error("arming a target that already has an upstream must not fire a command")
	}
	nm := next.(uiModel)
	if nm.picker != pickerConfirmDelete {
		t.Fatalf("picker = %v, want pickerConfirmDelete", nm.picker)
	}
}

// TestSecondDWithinWindowExecutesDelete confirms the double-press flow
// actually calls the injected DeleteFunc, exactly once, for the armed
// target.
func TestSecondDWithinWindowExecutesDelete(t *testing.T) {
	var gotWs model.Workspace
	calls := 0
	del := func(ws model.Workspace) (*trash.Entry, string, error) {
		calls++
		gotWs = ws
		return nil, "", nil
	}
	m := uiModel{
		ws: deleteFixture(), picker: pickerConfirmDelete,
		confirmTarget: deleteFixture()[0], confirmArmedAt: time.Now(),
		del: del,
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd == nil {
		t.Fatal("expected a command to run the delete")
	}
	nm := next.(uiModel)
	if nm.picker != pickerNone {
		t.Errorf("picker after confirming = %v, want pickerNone", nm.picker)
	}
	if !nm.deleting {
		t.Error("deleting must be true while the delete command is in flight")
	}
	msg := cmd()
	done, ok := msg.(deleteDoneMsg)
	if !ok {
		t.Fatalf("expected deleteDoneMsg, got %T", msg)
	}
	if calls != 1 {
		t.Errorf("del called %d times, want 1", calls)
	}
	if gotWs.Path != "/u/server-old" {
		t.Errorf("del called with %q, want /u/server-old", gotWs.Path)
	}
	if done.path != "/u/server-old" {
		t.Errorf("deleteDoneMsg.path = %q, want /u/server-old", done.path)
	}
}

// TestAnyOtherKeyCancelsArmedDelete covers the "any other key cancels"
// contract, and specifically that it never calls the DeleteFunc.
func TestAnyOtherKeyCancelsArmedDelete(t *testing.T) {
	called := false
	del := func(ws model.Workspace) (*trash.Entry, string, error) {
		called = true
		return nil, "", nil
	}
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("x")},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyEnter},
	} {
		m := uiModel{
			ws: deleteFixture(), picker: pickerConfirmDelete,
			confirmTarget: deleteFixture()[0], confirmArmedAt: time.Now(),
			del: del,
		}
		next, cmd := m.Update(msg)
		if cmd != nil {
			t.Errorf("%v: expected no command, got one", msg)
		}
		nm := next.(uiModel)
		if nm.picker != pickerNone {
			t.Errorf("%v: picker = %v, want pickerNone (cancelled)", msg, nm.picker)
		}
	}
	if called {
		t.Error("DeleteFunc must never be called by a cancelling key")
	}
}

// TestSecondDAfterWindowExpiresCancelsRatherThanDeletes is the timing half
// of "within a short window": a "d" that arrives after confirmWindow has
// elapsed must cancel, exactly like any other key, not execute a stale arm.
func TestSecondDAfterWindowExpiresCancelsRatherThanDeletes(t *testing.T) {
	called := false
	del := func(ws model.Workspace) (*trash.Entry, string, error) {
		called = true
		return nil, "", nil
	}
	m := uiModel{
		ws: deleteFixture(), picker: pickerConfirmDelete,
		confirmTarget:  deleteFixture()[0],
		confirmArmedAt: time.Now().Add(-(confirmWindow + time.Second)),
		del:            del,
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd != nil {
		t.Error("an expired second d must not return a delete command")
	}
	if next.(uiModel).picker != pickerNone {
		t.Error("an expired second d must cancel back to the dashboard")
	}
	if called {
		t.Error("DeleteFunc must never be called after the confirmation window expired")
	}
}

// TestDIsInertWhileADeleteIsInFlight guards against a stray second arm
// firing a concurrent delete while the first one hasn't reported back yet.
func TestDIsInertWhileADeleteIsInFlight(t *testing.T) {
	m := uiModel{ws: deleteFixture(), deleting: true}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd != nil {
		t.Error("d while a delete is in flight must not return a command")
	}
	if next.(uiModel).picker != pickerNone {
		t.Error("d while a delete is in flight must not arm a new confirmation")
	}
}

// TestApplyDeleteResultBlockedShowsReasonsAndKeepsWorkspace asserts a
// PruneBlockers refusal surfaces as a status line and never removes the
// workspace from the list.
func TestApplyDeleteResultBlockedShowsReasonsAndKeepsWorkspace(t *testing.T) {
	m := uiModel{ws: deleteFixture(), deleting: true}
	msg := deleteDoneMsg{
		path: "/u/server-dirty",
		err:  &trash.BlockedError{Blockers: []string{"has uncommitted changes"}},
	}
	next, cmd := m.applyDeleteResult(msg)
	if cmd != nil {
		t.Error("a blocked result must not trigger a background refresh")
	}
	nm := next.(uiModel)
	if nm.deleting {
		t.Error("deleting must be cleared after the result lands")
	}
	if nm.status == "" || !strings.Contains(nm.status, "uncommitted changes") {
		t.Errorf("status = %q, want it to mention the blocker", nm.status)
	}
	if len(nm.ws) != 2 {
		t.Errorf("workspace list must be untouched on a blocked delete, got %d entries", len(nm.ws))
	}
}

// TestApplyDeleteResultSuccessRemovesWorkspaceAndRecordsTrashEntry asserts
// the happy path: the deleted workspace disappears from the list
// immediately, a soft-delete's returned entry is added to the in-memory
// trash list, and (when refresh is set) a background refresh is kicked off.
func TestApplyDeleteResultSuccessRemovesWorkspaceAndRecordsTrashEntry(t *testing.T) {
	refreshCalled := false
	m := uiModel{
		ws: deleteFixture(),
		refresh: func() ([]model.Workspace, int) {
			refreshCalled = true
			return deleteFixture(), 0
		},
	}
	entry := trash.Entry{Path: "/u/server-old", Repo: "backend", Branch: "fix/whatever"}
	msg := deleteDoneMsg{path: "/u/server-old", entry: &entry}
	next, cmd := m.applyDeleteResult(msg)
	nm := next.(uiModel)
	if len(nm.ws) != 1 || nm.ws[0].Path != "/u/server-dirty" {
		t.Errorf("ws after delete = %+v, want only server-dirty left", nm.ws)
	}
	if len(nm.trash) != 1 || nm.trash[0].Path != "/u/server-old" {
		t.Errorf("trash after delete = %+v, want the new entry recorded", nm.trash)
	}
	if !nm.refreshing {
		t.Error("refreshing must be set true to kick a reconciling background refresh")
	}
	if cmd == nil {
		t.Fatal("expected a refresh command")
	}
	if _, ok := cmd().(refreshedMsg); !ok {
		t.Fatal("expected the command to produce refreshedMsg")
	}
	if !refreshCalled {
		t.Error("the injected refresh func must have been called")
	}
}

// TestUnpushedWarningNeverPushed asserts the no-upstream case: before the
// lazy count lands, the wording has no number; once applyUnpushedCount
// delivers it, the popup shows exactly how many commits are local-only.
func TestUnpushedWarningNeverPushed(t *testing.T) {
	ws := model.Workspace{Path: "/u/never-pushed", Branch: "feat/x", Kind: model.KindSibling}
	m := uiModel{picker: pickerConfirmDelete, confirmTarget: ws}

	if got := m.unpushedWarning(); got != "⚠ never pushed — commits exist only here" {
		t.Errorf("unpushedWarning (count not yet loaded) = %q", got)
	}

	m = m.applyUnpushedCount(unpushedCountMsg{path: ws.Path, count: 12, ok: true})
	if got := m.unpushedWarning(); got != "⚠ never pushed — 12 commits exist only here" {
		t.Errorf("unpushedWarning (count loaded) = %q, want the 12-commit wording", got)
	}

	plain := stripANSI(m.viewConfirmDelete(80, 24))
	if !strings.Contains(plain, "⚠ never pushed — 12 commits exist only here") {
		t.Errorf("rendered popup must contain the never-pushed warning with count, got:\n%s", plain)
	}
}

// TestUnpushedWarningSingularCommit covers the "1 commit" (no trailing s)
// wording edge case for both the never-pushed and ahead branches.
func TestUnpushedWarningSingularCommit(t *testing.T) {
	m := uiModel{
		picker:                     pickerConfirmDelete,
		confirmTarget:              model.Workspace{Path: "/u/x", Kind: model.KindSibling},
		confirmUnpushedCount:       1,
		confirmUnpushedCountLoaded: true,
	}
	if got, want := m.unpushedWarning(), "⚠ never pushed — 1 commit exists only here"; got != want {
		t.Errorf("unpushedWarning = %q, want %q", got, want)
	}

	m.confirmTarget.HasUpstream = true
	m.confirmTarget.Ahead = 1
	if got, want := m.unpushedWarning(), "⚠ 1 commit not pushed"; got != want {
		t.Errorf("unpushedWarning = %q, want %q", got, want)
	}
}

// TestUnpushedWarningAheadOfUpstream covers the second case: an upstream
// exists but HEAD is ahead of it. No lazy count is needed here -- Ahead is
// already known from FillStatus -- so the wording never depends on
// confirmUnpushedCountLoaded.
func TestUnpushedWarningAheadOfUpstream(t *testing.T) {
	ws := model.Workspace{Path: "/u/ahead", Branch: "feat/y", HasUpstream: true, Ahead: 3, Kind: model.KindSibling}
	m := uiModel{picker: pickerConfirmDelete, confirmTarget: ws}

	if got, want := m.unpushedWarning(), "⚠ 3 commits not pushed"; got != want {
		t.Errorf("unpushedWarning = %q, want %q", got, want)
	}

	plain := stripANSI(m.viewConfirmDelete(80, 24))
	if !strings.Contains(plain, "⚠ 3 commits not pushed") {
		t.Errorf("rendered popup must contain the ahead warning, got:\n%s", plain)
	}
}

// TestUnpushedWarningSilentWhenInSync is the negation: an upstream exists
// and HEAD matches it exactly (Ahead=0) -- there is genuinely nothing to
// warn about, and this is exactly the state a no-upstream branch could
// otherwise be confused with (see ParseStatusV2's doc comment), so this
// guards the fix actually landed.
func TestUnpushedWarningSilentWhenInSync(t *testing.T) {
	ws := model.Workspace{Path: "/u/synced", Branch: "feat/z", HasUpstream: true, Ahead: 0, Kind: model.KindSibling}
	m := uiModel{picker: pickerConfirmDelete, confirmTarget: ws}
	if got := m.unpushedWarning(); got != "" {
		t.Errorf("unpushedWarning = %q, want empty: fully in sync with upstream", got)
	}
}

// TestUnresolvedProcsWarningSilentWhenZero is the negation: no unresolved
// cwds means nothing to caution about, regardless of which workspace is
// armed.
func TestUnresolvedProcsWarningSilentWhenZero(t *testing.T) {
	m := uiModel{picker: pickerConfirmDelete, confirmTarget: deleteFixture()[0]}
	if got := m.unresolvedProcsWarning(); got != "" {
		t.Errorf("unresolvedProcsWarning = %q, want empty when unresolvedProcs is 0", got)
	}
}

// TestUnresolvedProcsWarningSingularAndPlural covers the "1 process" (no
// trailing s) wording edge case alongside the plural form.
func TestUnresolvedProcsWarningSingularAndPlural(t *testing.T) {
	m := uiModel{picker: pickerConfirmDelete, confirmTarget: deleteFixture()[0], unresolvedProcs: 1}
	if got, want := m.unresolvedProcsWarning(), "⚠ 1 process couldn't be checked"; got != want {
		t.Errorf("unresolvedProcsWarning(1) = %q, want %q", got, want)
	}
	m.unresolvedProcs = 3
	if got, want := m.unresolvedProcsWarning(), "⚠ 3 processes couldn't be checked"; got != want {
		t.Errorf("unresolvedProcsWarning(3) = %q, want %q", got, want)
	}
}

// TestUnresolvedProcsWarningIsGlobalNotPerTarget asserts the warning is
// unresolvedProcs alone, independent of which workspace is armed -- it is
// a fact about the last process snapshot as a whole (see
// unresolvedProcsWarning's own comment on why an unresolved cwd cannot be
// attributed to any one worktree), not a per-target property the way
// unpushedWarning is.
func TestUnresolvedProcsWarningIsGlobalNotPerTarget(t *testing.T) {
	fixtures := deleteFixture()
	for _, target := range fixtures {
		m := uiModel{picker: pickerConfirmDelete, confirmTarget: target, unresolvedProcs: 2}
		if got, want := m.unresolvedProcsWarning(), "⚠ 2 processes couldn't be checked"; got != want {
			t.Errorf("target %s: unresolvedProcsWarning = %q, want %q", target.Path, got, want)
		}
	}
}

// TestUnresolvedProcsWarningRendersAndNeverBlocks proves the warning
// actually reaches the rendered popup and, critically, never flips the
// popup into the blocked/refused state -- it must always leave "d again to
// confirm" and the DELETE? title in place. Warnings inform; only
// PruneBlockers refuses.
func TestUnresolvedProcsWarningRendersAndNeverBlocks(t *testing.T) {
	ws := model.Workspace{Path: "/u/clean", Branch: "feat/w", Kind: model.KindSibling, StatusKnown: true}
	m := uiModel{picker: pickerConfirmDelete, confirmTarget: ws, unresolvedProcs: 3}

	if blockers := ws.PruneBlockers(); len(blockers) != 0 {
		t.Fatalf("PruneBlockers = %v, want none: unresolved process probes must never block a delete", blockers)
	}

	plain := stripANSI(m.viewConfirmDelete(80, 24))
	if !strings.Contains(plain, "⚠ 3 processes couldn't be checked") {
		t.Errorf("rendered popup must contain the unresolved-procs warning, got:\n%s", plain)
	}
	if strings.Contains(plain, "BLOCKED") || strings.Contains(plain, "blocked:") {
		t.Errorf("popup must not present as blocked, got:\n%s", plain)
	}
	if !strings.Contains(plain, "d again to confirm") {
		t.Errorf("popup must still offer the confirm-again path, got:\n%s", plain)
	}
}

// TestUnpushedWarningsAreNeverBlockers is the safety-critical assertion:
// a workspace with unpushed work (either flavour) and no other problem
// must still show len(blockers)==0 and render "d again to confirm" --
// i.e. it stays fully deletable. Warnings inform; only PruneBlockers
// refuses.
func TestUnpushedWarningsAreNeverBlockers(t *testing.T) {
	cases := []struct {
		name string
		ws   model.Workspace
	}{
		{"never pushed", model.Workspace{Path: "/u/a", Kind: model.KindSibling, StatusKnown: true}},
		{"ahead of upstream", model.Workspace{Path: "/u/b", Kind: model.KindSibling, StatusKnown: true, HasUpstream: true, Ahead: 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if blockers := c.ws.PruneBlockers(); len(blockers) != 0 {
				t.Fatalf("PruneBlockers = %v, want none: unpushed work must never block a delete", blockers)
			}
			m := uiModel{picker: pickerConfirmDelete, confirmTarget: c.ws}
			plain := stripANSI(m.viewConfirmDelete(80, 24))
			if strings.Contains(plain, "BLOCKED") || strings.Contains(plain, "blocked:") {
				t.Errorf("popup must not present as blocked, got:\n%s", plain)
			}
			if !strings.Contains(plain, "d again to confirm") {
				t.Errorf("popup must still offer the confirm-again path, got:\n%s", plain)
			}
			if !strings.Contains(plain, "DELETE?") {
				t.Errorf("popup title must be DELETE?, not BLOCKED, got:\n%s", plain)
			}
		})
	}
}

// TestApplyDeleteResultHardDeleteInfoShowsBranchKept covers a hard delete
// whose branch survived (unmerged): no trash entry, but the info message
// must be shown.
func TestApplyDeleteResultHardDeleteInfoShowsBranchKept(t *testing.T) {
	m := uiModel{ws: deleteFixture()}
	msg := deleteDoneMsg{path: "/u/server-old", info: `worktree removed; branch "fix/whatever" kept (unmerged)`}
	next, _ := m.applyDeleteResult(msg)
	nm := next.(uiModel)
	if len(nm.trash) != 0 {
		t.Error("hard delete must not add a trash entry")
	}
	if !strings.Contains(nm.status, "kept") {
		t.Errorf("status = %q, want it to mention the kept branch", nm.status)
	}
}

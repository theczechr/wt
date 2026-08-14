package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/trash"
)

func trashFixture() []trash.Entry {
	now := time.Now()
	return []trash.Entry{
		{Path: "/u/server-old-thing", Repo: "backend", Branch: "fix/whatever", DeletedAt: now.Add(-31 * 24 * time.Hour)},
		{Path: "/u/admin-scratch", Repo: "dashboard", Branch: "feat/experiment", DeletedAt: now.Add(-45 * 24 * time.Hour)},
	}
}

// TestWhichKeyRoutesTToTrash covers the newly added "<space>t" entry point.
func TestWhichKeyRoutesTToTrash(t *testing.T) {
	m := uiModel{picker: pickerWhichKey}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if got := next.(uiModel).picker; got != pickerTrash {
		t.Errorf(`"t": picker = %v, want pickerTrash`, got)
	}
}

// TestTrashEscReturnsToDashboard mirrors TestFindEscReturnsToDashboard: the
// trash view is an additive overlay and must not disturb dashboard state.
func TestTrashEscReturnsToDashboard(t *testing.T) {
	m := uiModel{trash: trashFixture(), picker: pickerTrash, trashCursor: 1}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("Esc must not return a command")
	}
	nm := next.(uiModel)
	if nm.picker != pickerNone {
		t.Error("Esc must return to the dashboard")
	}
	if len(nm.trash) != 2 {
		t.Error("Esc must not disturb the trash list")
	}
}

// TestTrashNavigationJK covers j/k movement within the trash view.
func TestTrashNavigationJK(t *testing.T) {
	m := uiModel{trash: trashFixture(), picker: pickerTrash}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if got := next.(uiModel).trashCursor; got != 1 {
		t.Fatalf("j: cursor = %d, want 1", got)
	}
	next, _ = next.(uiModel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if got := next.(uiModel).trashCursor; got != 1 {
		t.Fatalf("j past the end: cursor = %d, want 1 (clamped)", got)
	}
	next, _ = next.(uiModel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if got := next.(uiModel).trashCursor; got != 0 {
		t.Fatalf("k: cursor = %d, want 0", got)
	}
}

// TestTrashURestoresSelectedEntry asserts "u" calls the injected RestoreFunc
// for exactly the entry under the cursor.
func TestTrashURestoresSelectedEntry(t *testing.T) {
	var got trash.Entry
	calls := 0
	restore := func(e trash.Entry) error {
		calls++
		got = e
		return nil
	}
	m := uiModel{trash: trashFixture(), picker: pickerTrash, trashCursor: 1, restore: restore}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	if cmd == nil {
		t.Fatal("expected a restore command")
	}
	if !next.(uiModel).trashBusy {
		t.Error("trashBusy must be true while the restore is in flight")
	}
	msg := cmd()
	done, ok := msg.(restoreDoneMsg)
	if !ok {
		t.Fatalf("expected restoreDoneMsg, got %T", msg)
	}
	if calls != 1 {
		t.Errorf("restore called %d times, want 1", calls)
	}
	if got.Path != "/u/admin-scratch" {
		t.Errorf("restore called with %q, want /u/admin-scratch (the row under the cursor)", got.Path)
	}
	if done.entry.Path != "/u/admin-scratch" {
		t.Errorf("restoreDoneMsg.entry.Path = %q, want /u/admin-scratch", done.entry.Path)
	}
}

// TestTrashDPurgesSelectedEntry mirrors the restore test for "D".
func TestTrashDPurgesSelectedEntry(t *testing.T) {
	var got trash.Entry
	purge := func(e trash.Entry) error {
		got = e
		return nil
	}
	m := uiModel{trash: trashFixture(), picker: pickerTrash, trashCursor: 0, purge: purge}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if cmd == nil {
		t.Fatal("expected a purge command")
	}
	if !next.(uiModel).trashBusy {
		t.Error("trashBusy must be true while the purge is in flight")
	}
	msg := cmd()
	if _, ok := msg.(purgeDoneMsg); !ok {
		t.Fatalf("expected purgeDoneMsg, got %T", msg)
	}
	if got.Path != "/u/server-old-thing" {
		t.Errorf("purge called with %q, want /u/server-old-thing", got.Path)
	}
}

// TestTrashUDInertWhileBusy guards against firing a second restore/purge
// before the first one has reported back.
func TestTrashUDInertWhileBusy(t *testing.T) {
	called := false
	restore := func(e trash.Entry) error { called = true; return nil }
	m := uiModel{trash: trashFixture(), picker: pickerTrash, trashBusy: true, restore: restore}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	if cmd != nil {
		t.Error("u while busy must not return a command")
	}
	if next.(uiModel).trashCursor != m.trashCursor {
		t.Error("u while busy must not otherwise change state")
	}
	if called {
		t.Error("RestoreFunc must not be called while already busy")
	}
}

// TestApplyRestoreResultSuccessRemovesEntryAndTriggersRefresh mirrors the
// delete-side test: a successful restore drops the entry from the
// in-memory trash list and kicks a background refresh so the recreated
// worktree shows up without a manual R.
func TestApplyRestoreResultSuccessRemovesEntryAndTriggersRefresh(t *testing.T) {
	refreshCalled := false
	fixture := trashFixture()
	m := uiModel{
		trash:     fixture,
		trashBusy: true,
		refresh:   func() ([]model.Workspace, int) { refreshCalled = true; return nil, 0 },
	}
	entry := fixture[0]
	next, cmd := m.applyRestoreResult(restoreDoneMsg{entry: entry})
	nm := next.(uiModel)
	if nm.trashBusy {
		t.Error("trashBusy must be cleared after the result lands")
	}
	if len(nm.trash) != 1 || nm.trash[0].Path != "/u/admin-scratch" {
		t.Errorf("trash after restore = %+v, want only admin-scratch left", nm.trash)
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

// TestApplyRestoreResultFailureKeepsEntryAndShowsReason asserts a failed
// restore (e.g. path occupied) leaves the trash entry in place.
func TestApplyRestoreResultFailureKeepsEntryAndShowsReason(t *testing.T) {
	fixture := trashFixture()
	m := uiModel{trash: fixture, trashBusy: true}
	entry := fixture[0]
	next, cmd := m.applyRestoreResult(restoreDoneMsg{entry: entry, err: errTest("already exists")})
	if cmd != nil {
		t.Error("a failed restore must not trigger a background refresh")
	}
	nm := next.(uiModel)
	if len(nm.trash) != 2 {
		t.Errorf("trash after a failed restore = %+v, want both entries kept", nm.trash)
	}
	if nm.status == "" {
		t.Error("expected a status message explaining the failure")
	}
}

// TestApplyPurgeResultSuccessRemovesEntryOnly asserts a purge drops the
// entry from the trash list and does NOT trigger any refresh -- purging
// never touches disk, so there is nothing for a refresh to reconcile.
func TestApplyPurgeResultSuccessRemovesEntryOnly(t *testing.T) {
	fixture := trashFixture()
	m := uiModel{
		trash:     fixture,
		trashBusy: true,
		refresh:   func() ([]model.Workspace, int) { t.Fatal("purge must never trigger a refresh"); return nil, 0 },
	}
	entry := fixture[1]
	next, cmd := m.applyPurgeResult(purgeDoneMsg{entry: entry})
	if cmd != nil {
		t.Error("purge must not return a command")
	}
	nm := next.(uiModel)
	if nm.trashBusy {
		t.Error("trashBusy must be cleared after the result lands")
	}
	if len(nm.trash) != 1 || nm.trash[0].Path != "/u/server-old-thing" {
		t.Errorf("trash after purge = %+v, want only server-old-thing left", nm.trash)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

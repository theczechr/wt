package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theczechr/wt/internal/model"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(m uiModel, keys ...string) uiModel {
	for _, k := range keys {
		next, _ := m.updateNormal(key(k))
		m = next.(uiModel)
	}
	return m
}

func withSessions(n int) uiModel {
	sessions := make([]model.Session, n)
	for i := range sessions {
		sessions[i] = model.Session{
			ID:    string(rune('a' + i)),
			Title: "session " + string(rune('a'+i)),
			Mtime: time.Now().Add(-time.Duration(i) * time.Hour),
		}
	}
	return uiModel{
		width:  140,
		height: 40,
		ws: []model.Workspace{
			{Repo: "server", Path: "/r/a", Branch: "feat/a", StatusKnown: true, Sessions: sessions},
			{Repo: "server", Path: "/r/b", Branch: "feat/b", StatusKnown: true},
		},
	}
}

// TestTabMovesFocusAndBack is the core of the feature: j/k must act on
// sessions while the pane is focused, and on worktrees otherwise.
func TestTabMovesFocusAndBack(t *testing.T) {
	m := withSessions(3)
	if m.focus != focusWorktrees {
		t.Fatal("dashboard did not start focused on the worktree list")
	}

	m = press(m, "tab")
	if m.focus != focusSessions {
		t.Fatal("tab did not focus the sessions pane")
	}

	m = press(m, "j", "j")
	if m.sessionCursor != 2 {
		t.Errorf("sessionCursor = %d, want 2", m.sessionCursor)
	}
	if m.cursor != 0 {
		t.Errorf("worktree cursor moved to %d while sessions had focus", m.cursor)
	}

	m = press(m, "tab")
	if m.focus != focusWorktrees {
		t.Error("tab did not return focus to the worktree list")
	}
	m = press(m, "j")
	if m.cursor != 1 {
		t.Errorf("worktree cursor = %d after returning focus, want 1", m.cursor)
	}
}

// TestSessionCursorResetsWhenWorktreeChanges guards a stale index: the
// sessions pane shows a different list once the worktree cursor moves, so a
// carried-over index could point past the end or at an unrelated session.
func TestSessionCursorResetsWhenWorktreeChanges(t *testing.T) {
	m := press(withSessions(3), "tab", "j", "j", "tab", "j")
	if m.sessionCursor != 0 {
		t.Errorf("sessionCursor = %d after changing worktree, want 0", m.sessionCursor)
	}
}

// TestTabRefusesWhenThereIsNothingToFocus covers both dead ends. Focusing an
// empty or undrawn pane leaves j/k apparently broken, so it must refuse and
// say why.
func TestTabRefusesWhenThereIsNothingToFocus(t *testing.T) {
	t.Run("no sessions", func(t *testing.T) {
		m := press(withSessions(0), "tab")
		if m.focus == focusSessions {
			t.Error("focused a pane with no sessions")
		}
		if m.status == "" {
			t.Error("refused silently; the user gets no explanation")
		}
	})

	t.Run("terminal too narrow", func(t *testing.T) {
		m := withSessions(3)
		m.width = narrowWidth - 1 // sessions pane is not drawn at all
		m = press(m, "tab")
		if m.focus == focusSessions {
			t.Error("focused a pane that is not rendered at this width")
		}
		if !strings.Contains(m.status, "width") {
			t.Errorf("status %q does not explain the width", m.status)
		}
	})
}

// TestEnterResumesTheSelectedSession is the whole point: r has always taken
// the newest idle session, and picking a specific one was impossible.
func TestEnterResumesTheSelectedSession(t *testing.T) {
	m := press(withSessions(3), "tab", "j", "j", "enter")
	if m.action != ActionResume {
		t.Fatalf("action = %v, want ActionResume", m.action)
	}
	if m.session != "c" {
		t.Errorf("resumed session %q, want the third one, %q", m.session, "c")
	}
	if m.chosen != "/r/a" {
		t.Errorf("chosen path = %q, want /r/a", m.chosen)
	}
}

// TestEscLeavesSessionFocus keeps Esc consistent with how it exits pickers.
func TestEscLeavesSessionFocus(t *testing.T) {
	if m := press(withSessions(2), "tab", "esc"); m.focus != focusWorktrees {
		t.Error("esc did not return focus to the worktree list")
	}
}

// TestFooterTracksFocus: while sessions have focus, j/k and enter mean
// something else, so a footer still advertising "cd" and "[ ] section"
// would be wrong.
func TestFooterTracksFocus(t *testing.T) {
	unfocused := withSessions(2).footerKeys()
	if !strings.Contains(unfocused, "tab sessions") || !strings.Contains(unfocused, "⏎ cd") {
		t.Errorf("worktree footer is missing its keys: %q", unfocused)
	}
	focused := press(withSessions(2), "tab").footerKeys()
	if strings.Contains(focused, "⏎ cd") {
		t.Errorf("sessions footer still claims enter means cd: %q", focused)
	}
	if !strings.Contains(focused, "resume this one") {
		t.Errorf("sessions footer does not say what enter does: %q", focused)
	}
}

// TestFocusMarkerOnlyWhenFocused: a cursor drawn in an unfocused pane
// claims j/k would move it, which is what Tab exists to change.
func TestFocusMarkerOnlyWhenFocused(t *testing.T) {
	m := withSessions(2)
	if strings.Contains(m.View(), "SESSIONS ◆") {
		t.Error("sessions pane rendered as focused before tab was pressed")
	}
	if !strings.Contains(press(m, "tab").View(), "SESSIONS ◆") {
		t.Error("focused sessions pane is not visually marked")
	}
}

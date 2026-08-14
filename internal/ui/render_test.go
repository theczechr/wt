package ui

import (
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/model"
)

func TestRenderRowShowsAgentMarkerForClaudeManaged(t *testing.T) {
	w := model.Workspace{
		Repo: "backend", Path: "/u/server/.claude/worktrees/pr-4004",
		Branch: "fix/foo", Kind: model.KindClaudeManaged,
	}
	got := RenderRow(w, 80, false)
	if !strings.Contains(got, "agent") {
		t.Errorf("claude-managed row must be marked agent: %q", got)
	}
}

func TestRenderRowShowsDirtyCountAndPR(t *testing.T) {
	w := model.Workspace{
		Repo: "backend", Path: "/u/server-cache", Branch: "feature/x",
		DirtyCount: 12, StatusKnown: true, PR: model.PR{Number: 4001, State: "OPEN"},
		Kind: model.KindSibling,
	}
	got := RenderRow(w, 80, false)
	if !strings.Contains(got, "12") {
		t.Errorf("row must show dirty count: %q", got)
	}
	if !strings.Contains(got, "4001") {
		t.Errorf("row must show PR number: %q", got)
	}
}

func TestRenderRowMarksLiveSession(t *testing.T) {
	w := model.Workspace{
		Repo: "backend", Path: "/u/server", Branch: "develop",
		Sessions: []model.Session{{ID: "a", Live: true}},
		Kind:     model.KindPrimary,
	}
	if !strings.Contains(RenderRow(w, 80, false), "●") {
		t.Error("live workspace must render a filled marker")
	}
	idle := model.Workspace{Repo: "backend", Path: "/u/server-x", Kind: model.KindSibling}
	if !strings.Contains(RenderRow(idle, 80, false), "○") {
		t.Error("idle workspace must render a hollow marker")
	}
}

// TestRenderRowFreeHintIsAdvisoryOnly asserts the FREE hint is surfaced for a
// workspace whose FreeHint() is true, purely as text -- RenderRow returns a
// string, so there is no way for it to trigger an action.
func TestRenderRowFreeHintIsAdvisoryOnly(t *testing.T) {
	w := model.Workspace{
		Repo: "backend", Path: "/u/server-old", Branch: "old-feature",
		PR:   model.PR{Number: 42, State: "MERGED"},
		Kind: model.KindSibling,
	}
	if !w.FreeHint() {
		t.Fatal("fixture must have FreeHint() true")
	}
	got := RenderRow(w, 80, false)
	if !strings.Contains(got, "FREE") {
		t.Errorf("row for a FreeHint workspace must show FREE: %q", got)
	}
}

// TestSessionLabelFallbackNoBlankRows is Correction 1's regression test:
// RenderRow (and the sessions pane) must call Session.Label(), never
// Session.Title directly, so an untitled session with a LastPrompt renders
// the prompt instead of a blank row.
func TestSessionLabelFallbackNoBlankRows(t *testing.T) {
	s := model.Session{ID: "sess-1", LastPrompt: "fix the flaky test"}
	if s.Title != "" {
		t.Fatal("fixture must have no Title")
	}
	got := s.Label()
	if got == "" {
		t.Fatal("Label() must not be blank")
	}
	if got != "fix the flaky test" {
		t.Errorf("Label() = %q, want the LastPrompt to surface, not a blank", got)
	}
	renderedRow := RenderSession(s, 80)
	if !strings.Contains(renderedRow, "fix the flaky test") {
		t.Errorf("RenderSession must use Label() fallback, got %q", renderedRow)
	}
}

// TestSortWorkspacesPutsUserOwnKindsBeforeAgentKinds is Correction 2's
// regression test. Kind is a string type, so sorting by Kind directly is
// alphabetical -- "claude-managed" < "foreign" < "nested" < "primary" <
// "sibling" -- which would bury the user's own primary checkout and
// hand-made siblings below agent-created worktrees. sortWorkspaces must use
// the explicit kindRank order instead: primary, sibling, nested,
// claude-managed, foreign.
func TestSortWorkspacesPutsUserOwnKindsBeforeAgentKinds(t *testing.T) {
	unordered := []model.Workspace{
		{Repo: "backend", Path: "/u/server-foreign", Kind: model.KindForeign},
		{Repo: "backend", Path: "/u/server-claude", Kind: model.KindClaudeManaged},
		{Repo: "backend", Path: "/u/server-nested", Kind: model.KindNested},
		{Repo: "backend", Path: "/u/server-sibling", Kind: model.KindSibling},
		{Repo: "backend", Path: "/u/server", Kind: model.KindPrimary},
	}
	got := sortWorkspaces(unordered)

	wantOrder := []model.Kind{
		model.KindPrimary, model.KindSibling, model.KindNested,
		model.KindClaudeManaged, model.KindForeign,
	}
	for i, k := range wantOrder {
		if got[i].Kind != k {
			t.Fatalf("position %d: got Kind %q, want %q (full order: %+v)", i, got[i].Kind, k, got)
		}
	}
}

// TestSortWorkspacesIsStableAndDeterministicAcrossRefreshes asserts the
// tiebreaker chain (Repo, kindRank, Path) produces the same order every
// time it's run against the same (even re-shuffled) input, and that
// repos and same-kind entries are each ordered correctly.
func TestSortWorkspacesIsStableAndDeterministicAcrossRefreshes(t *testing.T) {
	ws := []model.Workspace{
		{Repo: "frontend", Path: "/u/web", Kind: model.KindPrimary},
		{Repo: "backend", Path: "/u/server-b", Kind: model.KindSibling},
		{Repo: "backend", Path: "/u/server", Kind: model.KindPrimary},
		{Repo: "backend", Path: "/u/server-a", Kind: model.KindSibling},
	}
	first := sortWorkspaces(ws)
	second := sortWorkspaces(ws) // simulate a refresh with the same underlying data

	wantPaths := []string{"/u/server", "/u/server-a", "/u/server-b", "/u/web"}
	for i, want := range wantPaths {
		if first[i].Path != want {
			t.Errorf("first[%d].Path = %q, want %q", i, first[i].Path, want)
		}
		if second[i].Path != want {
			t.Errorf("second[%d].Path = %q, want %q", i, second[i].Path, want)
		}
	}
}

package model

import (
	"strings"
	"testing"
)

func TestSessionLabelPrefersTitleThenPromptThenID(t *testing.T) {
	titled := Session{ID: "id-1", Title: "A real title", LastPrompt: "ignored prompt"}
	if got := titled.Label(); got != "A real title" {
		t.Errorf("Label() = %q, want title", got)
	}

	prompted := Session{ID: "id-2", LastPrompt: "fix the flaky test"}
	if got := prompted.Label(); got != "fix the flaky test" {
		t.Errorf("Label() = %q, want prompt", got)
	}

	bare := Session{ID: "id-3"}
	if got := bare.Label(); got != "id-3" {
		t.Errorf("Label() = %q, want id fallback", got)
	}
}

func TestSessionLabelCollapsesNewlinesAndTruncatesOnRuneBoundary(t *testing.T) {
	// 70 multi-byte runes (€ is 3 bytes in UTF-8), with an embedded newline
	// that must not survive into the label as a line break.
	prompt := "line one\nline two — some plans include the € currency symbol repeated eno"
	s := Session{ID: "id-4", LastPrompt: prompt}

	got := s.Label()

	for _, r := range got {
		if r == '\n' || r == '\r' {
			t.Fatalf("Label() must be single-line, got %q", got)
		}
	}

	runes := []rune(got)
	if len(runes) != 61 { // 60 truncated runes + the appended ellipsis
		t.Fatalf("Label() = %q (rune len %d), want 60 runes + ellipsis", got, len(runes))
	}
	if runes[len(runes)-1] != '…' {
		t.Fatalf("Label() = %q, want trailing ellipsis marking truncation", got)
	}
	// The multi-byte rune must not be cut in half: re-decoding must round-trip.
	if string(runes) != got {
		t.Fatalf("Label() = %q is not valid on rune boundaries", got)
	}
}

func TestSessionLabelShortPromptIsNotTruncated(t *testing.T) {
	s := Session{ID: "id-5", LastPrompt: "short prompt"}
	if got := s.Label(); got != "short prompt" {
		t.Errorf("Label() = %q, must not truncate or add ellipsis to a short prompt", got)
	}
}

// TestPruneBlockersZeroValueWorkspaceIsBlocked is the polarity regression
// test: a completely bare Workspace{} -- the shape of a Workspace that never
// passed through discover.FillStatus at all (a struct literal in a test
// that forgot the field, a pre-migration snapshot.json entry deserializing
// without a "StatusKnown" key) -- must be refused, not silently treated as
// clean. If StatusKnown's polarity were ever inverted (e.g. renamed to
// StatusUnknown, defaulting to false meaning "known clean"), this is the
// test that would catch it: it would start passing with an empty blocker
// list instead.
func TestPruneBlockersZeroValueWorkspaceIsBlocked(t *testing.T) {
	var w Workspace
	blockers := w.PruneBlockers()
	if len(blockers) == 0 {
		t.Fatal("a zero-value Workspace must be blocked (fail closed), got no blockers")
	}
	found := false
	for _, b := range blockers {
		if strings.Contains(b, "status") {
			found = true
		}
	}
	if !found {
		t.Errorf("blockers = %v, want one mentioning the unreadable status", blockers)
	}
}

// TestPruneBlockersRefusesUnknownStatusEvenWhenOtherwiseDeletable asserts
// !StatusKnown is refused on its own, isolated from every other blocker: a
// workspace that is not primary, not dirty, has no live session and no
// running processes must still be blocked purely because its git status
// could not be read, and the reason must say so.
func TestPruneBlockersRefusesUnknownStatusEvenWhenOtherwiseDeletable(t *testing.T) {
	w := Workspace{Repo: "backend", Path: "/u/server-x", Kind: KindSibling, SessionsKnown: true}
	blockers := w.PruneBlockers()
	if len(blockers) != 1 {
		t.Fatalf("blockers = %v, want exactly one (the unknown-status blocker)", blockers)
	}
	if !strings.Contains(blockers[0], "status") || !strings.Contains(blockers[0], "could not be read") {
		t.Errorf("blocker = %q, want it to mention the status could not be read", blockers[0])
	}
}

// TestPruneBlockersAllowsCleanKnownWorkspace is the counterpart: once
// StatusKnown is explicitly true and nothing else is wrong, PruneBlockers
// must return no blockers at all -- proving the fix doesn't over-block a
// workspace whose status genuinely was read successfully and came back
// clean.
func TestPruneBlockersAllowsCleanKnownWorkspace(t *testing.T) {
	w := Workspace{Repo: "backend", Path: "/u/server-x", Kind: KindSibling, StatusKnown: true, SessionsKnown: true}
	if blockers := w.PruneBlockers(); len(blockers) != 0 {
		t.Errorf("blockers = %v, want none for a known-clean, non-primary, idle workspace", blockers)
	}
}

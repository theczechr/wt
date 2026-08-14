package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/theczechr/wt/internal/model"
)

// TestElideAroundMatchStaysWithinBudget covers elideAroundMatch's core
// invariant: before+match+after must never exceed the requested budget,
// whether the match sits mid-text, at the very start, at the very end, or
// is itself wider than the whole budget.
func TestElideAroundMatchStaysWithinBudget(t *testing.T) {
	text := "some long prompt text about refunds and refunds and disputes and other things"
	start, end, ok := findMatchRuneRange(text, "refunds")
	if !ok {
		t.Fatal("test fixture setup: expected a match")
	}

	for _, budget := range []int{3, 10, 20, 40, 100} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			before, match, after := elideAroundMatch(text, start, end, budget)
			total := lipgloss.Width(before) + lipgloss.Width(match) + lipgloss.Width(after)
			if total > budget {
				t.Errorf("total width %d exceeds budget %d (before=%q match=%q after=%q)", total, budget, before, match, after)
			}
		})
	}
}

// TestElideAroundMatchAtTextBoundaries covers a match at the very start
// (no "before" context) and the very end (no "after" context) -- both must
// still respect the budget and must not panic on empty context slices.
func TestElideAroundMatchAtTextBoundaries(t *testing.T) {
	text := "refund at the very start of this text"
	start, end, _ := findMatchRuneRange(text, "refund")
	before, match, after := elideAroundMatch(text, start, end, 30)
	if before != "" {
		t.Errorf("a match at position 0 must have no leading context, got %q", before)
	}
	if match != "refund" {
		t.Errorf("match = %q, want refund", match)
	}
	if lipgloss.Width(before)+lipgloss.Width(match)+lipgloss.Width(after) > 30 {
		t.Error("total width exceeds budget")
	}

	text2 := "this text ends with the word refund"
	start2, end2, _ := findMatchRuneRange(text2, "refund")
	before2, match2, after2 := elideAroundMatch(text2, start2, end2, 30)
	if after2 != "" {
		t.Errorf("a match at the very end must have no trailing context, got %q", after2)
	}
	if lipgloss.Width(before2)+lipgloss.Width(match2)+lipgloss.Width(after2) > 30 {
		t.Error("total width exceeds budget")
	}
	if match2 != "refund" {
		t.Errorf("match = %q, want refund", match2)
	}
}

// TestTailElideKeepsSuffixWithinBudget mirrors truncateEllipsis's own
// contract (render_width_test.go covers that one) for the head-trimmed
// mirror this package adds.
func TestTailElideKeepsSuffixWithinBudget(t *testing.T) {
	s := "the quick brown fox jumps over the lazy dog"
	for _, budget := range []int{0, 1, 5, 10, 100} {
		got := tailElide(s, budget)
		if w := lipgloss.Width(got); w > budget {
			t.Errorf("tailElide(_, %d) = %q, width %d exceeds budget", budget, got, w)
		}
	}
	if got := tailElide(s, 100); got != s {
		t.Errorf("a budget wider than s must return s unchanged, got %q", got)
	}
	if got := tailElide(s, 8); !strings.HasSuffix(got, "zy dog") {
		t.Errorf("tailElide must keep the SUFFIX of s, got %q", got)
	}
	if got := tailElide(s, 8); !strings.HasPrefix(got, "…") {
		t.Errorf("a truncated tailElide must be prefixed with the ellipsis, got %q", got)
	}
}

// TestRenderGrepHitHeaderAndExcerptExactWidth is this file's width-exactness
// regression test, mirroring TestRenderRowExactWidthWhenStyled: both grep
// row renderers must produce a string whose visible (lipgloss.Width) width
// equals the requested width exactly, under real ANSI styling, across a
// range of widths including ones narrower than the fixed columns.
func TestRenderGrepHitHeaderAndExcerptExactWidth(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	h := grepHit{
		Workspace: model.Workspace{Repo: "backend", Path: "/u/server-cache"},
		Session:   model.Session{ID: "sess1", Title: "Fix the refund bug", PRNumber: 4001, Live: true},
		SessionID: "sess1",
		Text:      strings.Repeat("some long prompt text about refunds and refunds ", 4),
	}
	h.MatchStart, h.MatchEnd, _ = findMatchRuneRange(h.Text, "refunds")

	untracked := grepHit{Untracked: true, SessionID: "unknown", Text: "a refund related untracked hit"}
	untracked.MatchStart, untracked.MatchEnd, _ = findMatchRuneRange(untracked.Text, "refund")

	// 45 is deliberately the narrowest width tested here (renderGrepHitHeader's
	// fixed non-label columns -- marker+name+time+PR+separators -- total 42,
	// the same "fixed columns assume a realistically-sized pane" precedent
	// RenderRow itself relies on; TestRenderRowExactWidthWhenStyled's own
	// narrowest case is 40, similarly chosen above RenderRow's fixed floor).
	// Only the flexible label column absorbs a narrow pane. The width
	// contract that actually matters (nothing wraps at the terminal widths
	// wt runs at) is covered at 80/100/160 by TestPickerOverlaysExactWidth
	// in render_width_test.go.
	for _, width := range []int{45, 60, 78, 98, 158} {
		for _, selected := range []bool{false, true} {
			for _, hit := range []grepHit{h, untracked} {
				got := renderGrepHitHeader(hit, selected, width)
				if w := lipgloss.Width(got); w != width {
					t.Errorf("renderGrepHitHeader(width=%d selected=%v untracked=%v): lipgloss.Width = %d", width, selected, hit.Untracked, w)
				}
				got = renderGrepHitExcerpt(hit, width)
				if w := lipgloss.Width(got); w != width {
					t.Errorf("renderGrepHitExcerpt(width=%d untracked=%v): lipgloss.Width = %d", width, hit.Untracked, w)
				}
			}
		}
	}
}

// TestRenderGrepHitExcerptHighlightsAfterTruncation asserts the highlighted
// match substring survives styling+width-fitting -- the same regression
// class TestRenderRowExactWidthWhenStyled guards against for the main
// list's "⚠ agent" tag, applied here to grep's highlight.
func TestRenderGrepHitExcerptHighlightsAfterTruncation(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	h := grepHit{Text: "please fix the refund bug in the queue processor"}
	h.MatchStart, h.MatchEnd, _ = findMatchRuneRange(h.Text, "refund")

	got := renderGrepHitExcerpt(h, 60)
	plain := stripANSI(got)
	if !strings.Contains(plain, "refund") {
		t.Errorf("excerpt must still contain the matched word after styling: %q", plain)
	}
	if w := lipgloss.Width(got); w != 60 {
		t.Errorf("lipgloss.Width = %d, want 60", w)
	}
}

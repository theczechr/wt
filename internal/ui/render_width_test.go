package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/trash"
)

// TestRenderRowExactWidthWhenStyled is the regression test this package's
// styling pass exists to never fail again: an earlier version's column
// widths overflowed the pane once ANSI codes entered the picture, silently
// dropping the "⚠ agent" tag before truncation ever reached it. RenderRow
// must produce a string whose *visible* width -- lipgloss.Width, which is
// ANSI-aware -- equals the requested width exactly, for both a short and a
// very long (overflowing) branch name, and with real colour turned on, not
// the Ascii no-op profile TestMain forces for the rest of this package.
func TestRenderRowExactWidthWhenStyled(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	long := strings.Repeat("feature/a-genuinely-extremely-long-branch-name-", 4) + "end"

	cases := []struct {
		name     string
		w        model.Workspace
		width    int
		selected bool
	}{
		{
			name: "short branch, unselected",
			w: model.Workspace{
				Repo: "backend", Path: "/u/server-cache", Branch: "feature/x",
				DirtyCount: 3, PR: model.PR{Number: 4001, State: "OPEN"},
				Kind: model.KindSibling,
			},
			width: 80,
		},
		{
			name: "long branch, unselected",
			w: model.Workspace{
				Repo: "backend", Path: "/u/server-cache", Branch: long,
				DirtyCount: 3, PR: model.PR{Number: 4001, State: "OPEN"},
				Kind: model.KindSibling,
			},
			width: 80,
		},
		{
			name: "long branch, selected, agent tag, free hint",
			w: model.Workspace{
				Repo: "backend", Path: "/u/.claude/worktrees/pr-4004", Branch: long,
				PR:   model.PR{Number: 42, State: "MERGED"},
				Kind: model.KindClaudeManaged,
			},
			width:    80,
			selected: true,
		},
		{
			name: "long branch, narrow pane",
			w: model.Workspace{
				Repo: "backend", Path: "/u/.sprout/sprout-1145141", Branch: long,
				PR:   model.PR{Number: 42, State: "MERGED"},
				Kind: model.KindForeign,
			},
			width: 40,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RenderRow(c.w, c.width, c.selected)
			if w := lipgloss.Width(got); w != c.width {
				t.Errorf("lipgloss.Width(RenderRow(...)) = %d, want %d\nrendered: %q", w, c.width, got)
			}
			// The "⚠ agent" tag must survive styling+truncation whenever
			// there's room for it -- this is the exact regression: it must
			// never be silently dropped by column-width math that only
			// worked before ANSI codes entered the string.
			if c.w.Kind == model.KindClaudeManaged && c.width >= 40 {
				plain := stripANSI(got)
				if !strings.Contains(plain, "agent") {
					t.Errorf("styled row must still show the agent tag: %q", plain)
				}
			}
		})
	}
}

// TestRenderRepoHeaderExactWidthWhenNumbered is render_width_test.go's
// extension for the section-jump feature: the "N " section-number prefix
// changes the header's plain-text length, and this package's whole styling
// discipline (see the package doc comment) is that every width decision
// happens on plain text before Render -- so an added prefix must never push
// the styled header past the pane width. Covers a numbered header (1-9), an
// unnumbered one (section 10, past the 1-9 jump-key range), and both against
// a pane too narrow for the label to fit at all (forcing truncation).
func TestRenderRepoHeaderExactWidthWhenNumbered(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	cases := []struct {
		name    string
		repo    string
		count   int
		section int
		width   int
	}{
		{"numbered, roomy width", "dashboard", 14, 1, 80},
		{"numbered, second section", "backend", 43, 2, 80},
		{"numbered, ninth section", "frontend", 14, 9, 80},
		{"unnumbered, section past 9", "frontend", 14, 10, 80},
		{"numbered, narrow pane forces truncation", "dashboard", 14, 1, 12},
		{"numbered, extremely narrow pane", "backend", 43, 3, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderRepoHeader(c.repo, c.count, c.section, c.width)
			if w := lipgloss.Width(got); w != c.width {
				t.Errorf("lipgloss.Width(renderRepoHeader(...)) = %d, want %d\nrendered: %q", w, c.width, got)
			}
			plain := stripANSI(got)
			if c.section >= 1 && c.section <= 9 && c.width >= 20 {
				want := fmt.Sprintf("%d %s", c.section, c.repo)
				if !strings.Contains(plain, want) {
					t.Errorf("header must show section number %d next to repo name: got %q, want to contain %q", c.section, plain, want)
				}
			}
			if c.section > 9 && c.width >= 20 {
				if strings.Contains(plain, fmt.Sprintf("%d %s", c.section, c.repo)) {
					t.Errorf("section %d is past the 1-9 jump range and must render with no number: %q", c.section, plain)
				}
			}
		})
	}
}

// TestPickerOverlaysExactWidth is the picker equivalent of
// TestRenderRowExactWidthWhenStyled: every line of each overlay's View()
// must measure exactly the terminal width via lipgloss.Width, under real
// ANSI styling, at 80/100/160 columns (spanning the narrow-pane cutoff at
// 100) and two heights. The footer line below each pane is intentionally
// excluded -- it was never subject to exact-width in the pre-existing
// dashboard footer either (see View()'s own un-padded footer) -- only the
// bordered pane itself (or, for the which-key popup, the whole
// lipgloss.Place'd canvas) must never wrap.
func TestPickerOverlaysExactWidth(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	ws := []model.Workspace{
		{
			Repo: "backend", Path: "/u/server-cache",
			Branch: "feature/a-very-long-branch-name-that-would-otherwise-overflow-the-pane",
			PR:     model.PR{Number: 4001, State: "OPEN"}, DirtyCount: 3,
		},
		{Repo: "backend", Path: "/u/server", Branch: "develop"},
	}

	longText := strings.Repeat("some long prompt text about refunds and refunds and disputes ", 5)
	trackedHit := grepHit{
		Workspace: ws[0],
		Session:   model.Session{ID: "s1", Title: "Fix the refund bug", Live: true, PRNumber: 4001},
		SessionID: "s1",
		Text:      longText,
	}
	trackedHit.MatchStart, trackedHit.MatchEnd, _ = findMatchRuneRange(longText, "refunds")
	untrackedHit := grepHit{
		Untracked: true, SessionID: "unknown",
		Text: "refund related text in an untracked worktree",
	}
	untrackedHit.MatchStart, untrackedHit.MatchEnd, _ = findMatchRuneRange(untrackedHit.Text, "refund")
	hits := []grepHit{trackedHit, untrackedHit}

	for _, width := range []int{80, 100, 160} {
		for _, height := range []int{24, 40} {
			base := uiModel{ws: ws, width: width, height: height}

			t.Run(fmt.Sprintf("whichkey/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerWhichKey
				assertExactWidthLines(t, m.View(), width, -1) // the whole canvas is the popup; check every line
			})
			t.Run(fmt.Sprintf("find/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerFind
				m.findQuery = "backend"
				assertExactWidthLines(t, m.View(), width, height-3)
			})
			t.Run(fmt.Sprintf("grep-results/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerGrep
				m.grep = grepState{query: "refunds", hits: hits, scanned: 42}
				assertExactWidthLines(t, m.View(), width, height-3)
			})
			t.Run(fmt.Sprintf("grep-searching/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerGrep
				m.grep = grepState{query: "p", searching: true}
				assertExactWidthLines(t, m.View(), width, height-3)
			})
			t.Run(fmt.Sprintf("grep-error/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerGrep
				m.grep = grepState{err: ErrRipgrepNotFound}
				assertExactWidthLines(t, m.View(), width, height-3)
			})
		}
	}
}

// assertExactWidthLines checks that every line of a picker's bordered-pane
// region measures exactly width columns via lipgloss.Width -- i.e. nothing
// wrapped onto a second physical line. paneLines is the number of leading
// lines that make up the pane (View()'s output is pane + "\n\n" + footer +
// "\n", so the footer/blank separator are excluded by construction); pass
// -1 to check every line (the which-key popup has no separate footer --
// its View() IS the pane, centred on the full canvas via lipgloss.Place).
func assertExactWidthLines(t *testing.T, view string, width, paneLines int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if paneLines >= 0 && paneLines < len(lines) {
		lines = lines[:paneLines]
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != width {
			t.Errorf("line %d: lipgloss.Width = %d, want %d\nline: %q", i, w, width, l)
		}
	}
}

// TestDeleteConfirmAndTrashOverlaysExactWidth extends
// TestPickerOverlaysExactWidth to the two overlays this package's
// delete/trash feature adds: the "d d" confirmation popup (both the normal
// case and the blocked case -- most of the user's real worktrees have a
// live session or dirty files, so the blocked rendering is the common case,
// not an edge case) and the trash view (populated and empty). Same
// contract: every line must measure exactly the terminal width via
// lipgloss.Width, under real ANSI styling, at 80/100/160 columns and two
// heights -- nothing may wrap onto a second physical line.
func TestDeleteConfirmAndTrashOverlaysExactWidth(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	clean := model.Workspace{
		Repo: "backend", Path: "/Users/alice/code/server-old-thing",
		Branch: "fix/a-very-long-branch-name-that-would-otherwise-overflow-the-popup",
		Kind:   model.KindSibling, StatusKnown: true,
	}
	blocked := model.Workspace{
		Repo: "backend", Path: "/Users/alice/code/server",
		Branch: "feature/refund-payment-queue",
		Kind:   model.KindPrimary, DirtyCount: 3,
		Sessions: []model.Session{{ID: "s1", Live: true}},
	}
	neverPushed := model.Workspace{
		Repo: "backend", Path: "/Users/alice/code/server-scratch",
		Branch: "feat/a-very-long-branch-name-that-would-otherwise-overflow-the-popup-warning-row",
		Kind:   model.KindSibling, StatusKnown: true, HasUpstream: false,
	}
	aheadOfUpstream := model.Workspace{
		Repo: "backend", Path: "/Users/alice/code/server-ahead",
		Branch: "feature/a-very-long-branch-name-that-would-otherwise-overflow-the-popup-warning-row",
		Kind:   model.KindSibling, StatusKnown: true, HasUpstream: true, Ahead: 128,
	}

	now := time.Now()
	trashEntries := []trash.Entry{
		{Path: "/u/server-old-thing", Repo: "backend", Branch: "fix/whatever", DeletedAt: now.Add(-31 * 24 * time.Hour)},
		{Path: "/u/admin-scratch", Repo: "dashboard", Branch: "feat/a-very-long-experiment-branch-name-here", DeletedAt: now.Add(-45 * 24 * time.Hour)},
	}

	for _, width := range []int{80, 100, 160} {
		for _, height := range []int{24, 40} {
			base := uiModel{width: width, height: height}

			t.Run(fmt.Sprintf("confirm-delete-soft/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerConfirmDelete
				m.confirmTarget = clean
				m.trashMode = "soft"
				assertExactWidthLines(t, m.View(), width, -1)
			})
			t.Run(fmt.Sprintf("confirm-delete-hard/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerConfirmDelete
				m.confirmTarget = clean
				m.trashMode = "hard"
				assertExactWidthLines(t, m.View(), width, -1)
			})
			t.Run(fmt.Sprintf("confirm-delete-blocked/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerConfirmDelete
				m.confirmTarget = blocked
				assertExactWidthLines(t, m.View(), width, -1)
			})
			t.Run(fmt.Sprintf("confirm-delete-never-pushed/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerConfirmDelete
				m.confirmTarget = neverPushed
				// Loaded, 3-digit count: the longest realistic form of this
				// warning row, so this is the case that actually stresses
				// truncation at narrow widths.
				m.confirmUnpushedCount = 128
				m.confirmUnpushedCountLoaded = true
				got := m.View()
				assertExactWidthLines(t, got, width, -1)
				plain := stripANSI(got)
				if !strings.Contains(plain, "⚠ never pushed — 128 commits exist only here") {
					t.Errorf("popup must render the never-pushed warning with its count, got:\n%s", plain)
				}
			})
			t.Run(fmt.Sprintf("confirm-delete-ahead/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerConfirmDelete
				m.confirmTarget = aheadOfUpstream
				got := m.View()
				assertExactWidthLines(t, got, width, -1)
				plain := stripANSI(got)
				if !strings.Contains(plain, "⚠ 128 commits not pushed") {
					t.Errorf("popup must render the ahead-of-upstream warning, got:\n%s", plain)
				}
			})
			t.Run(fmt.Sprintf("confirm-delete-unresolved-procs/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerConfirmDelete
				m.confirmTarget = clean
				// Double digits: the longest realistic form of this warning
				// row on its own, so this is the case that actually
				// stresses truncation at narrow widths.
				m.unresolvedProcs = 12
				got := m.View()
				assertExactWidthLines(t, got, width, -1)
				plain := stripANSI(got)
				if !strings.Contains(plain, "⚠ 12 processes couldn't be checked") {
					t.Errorf("popup must render the unresolved-procs warning, got:\n%s", plain)
				}
			})
			t.Run(fmt.Sprintf("confirm-delete-ahead-plus-unresolved-procs/%dx%d", width, height), func(t *testing.T) {
				// Both warnings stacked at once -- the longest real combined
				// popup this feature produces, and the case most likely to
				// overflow at 80 columns since it adds a whole extra row.
				m := base
				m.picker = pickerConfirmDelete
				m.confirmTarget = aheadOfUpstream
				m.unresolvedProcs = 12
				got := m.View()
				assertExactWidthLines(t, got, width, -1)
				plain := stripANSI(got)
				if !strings.Contains(plain, "⚠ 128 commits not pushed") {
					t.Errorf("popup must still render the ahead-of-upstream warning, got:\n%s", plain)
				}
				if !strings.Contains(plain, "⚠ 12 processes couldn't be checked") {
					t.Errorf("popup must render the unresolved-procs warning, got:\n%s", plain)
				}
			})
			t.Run(fmt.Sprintf("trash-populated/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerTrash
				m.trash = trashEntries
				assertExactWidthLines(t, m.View(), width, height-3)
			})
			t.Run(fmt.Sprintf("trash-empty/%dx%d", width, height), func(t *testing.T) {
				m := base
				m.picker = pickerTrash
				assertExactWidthLines(t, m.View(), width, height-3)
			})
		}
	}
}

// stripANSI removes CSI-style ANSI escape sequences so a styled string's
// remaining text can be asserted on directly, without lipgloss.Width lying
// about it (this is a test-only convenience -- production code must never
// need to strip ANSI to reason about content, only to measure width, and
// only via lipgloss.Width).
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

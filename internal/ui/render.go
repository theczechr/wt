// Package ui renders the worktree dashboard.
//
// Styling discipline (see styles.go for the palette): every function in
// this file computes field widths, padding and truncation on plain,
// unstyled text first, and calls a lipgloss style's Render only as the very
// last step, on an already-correctly-sized piece. Two things make this
// non-negotiable:
//
//  1. ANSI escape sequences do not count toward lipgloss.Width, but they do
//     count toward len() and utf8.RuneCountInString. Measuring an
//     already-styled string with either of those silently produces the
//     wrong width. All width math here goes through lipgloss.Width, and
//     only ever on plain text.
//  2. lipgloss.Style.Render wraps its whole argument in one SGR sequence
//     ending in a full reset. Nesting one Render call's output inside
//     another (or inside a lipgloss.Style with Width() set, which
//     word-wraps) risks that inner reset clobbering an outer style, or the
//     outer wrapping the row onto a second line. So styled segments here
//     are always rendered independently and concatenated, never nested one
//     inside another.
package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/theczechr/wt/internal/model"
)

// kindRank orders workspaces so the user's own checkouts sort above
// anything an agent created. Kind is a string type ("claude-managed" <
// "foreign" < "nested" < "primary" < "sibling" alphabetically), which is
// backwards for a tool whose whole point is finding the user's own
// workspaces -- so sorting must go through this explicit rank instead of
// comparing Kind values directly.
func kindRank(k model.Kind) int {
	switch k {
	case model.KindPrimary:
		return 0
	case model.KindSibling:
		return 1
	case model.KindNested:
		return 2
	case model.KindClaudeManaged:
		return 3
	case model.KindForeign:
		return 4
	default:
		return 5
	}
}

// Column widths for RenderRow. Branch is deliberately absent: it gets
// whatever width is left over once everything else (including the tag,
// which carries FREE/agent -- the two things the user scans the pane for)
// has taken its share of width, so a long branch name can never silently
// push those past the edge of the pane.
const (
	rowNameWidth  = 20
	rowPRWidth    = 7
	rowDirtyWidth = 4
)

// tagPart is one advisory badge in the trailing tag column, coloured
// independently of its neighbour so "⚠ agent" (muted) and "FREE" (bold
// green) stay visually distinct when both apply to the same row.
type tagPart struct {
	text string
	role role
}

// RenderRow formats one worktree as a single, exactly-`width`-column styled
// line (lipgloss.Width(result) == width whenever width > 0). selected paints
// the whole row -- prefix aside, which the caller owns -- with the
// selected-row background, not just the text: every field is padded to its
// column width in plain text before styling, so the background style wraps
// the padding too.
func RenderRow(w model.Workspace, width int, selected bool) string {
	if width <= 0 {
		return ""
	}

	markerGlyph, markerRole := rowMarker(w)
	name := padField(displayName(w), rowNameWidth)
	prText, prRole := rowPR(w)
	pr := padField(prText, rowPRWidth)
	dirty := padField(rowDirty(w), rowDirtyWidth)
	tagParts := rowTagParts(w)
	tagText := tagPlainText(tagParts)

	// marker + name + pr + dirty + the 4 separators between them (marker-name,
	// name-branch, branch-pr, pr-dirty) are non-negotiable; branch and the
	// tag split whatever's left of width, tag first.
	fixed := lipgloss.Width(markerGlyph) + lipgloss.Width(name) + lipgloss.Width(pr) + lipgloss.Width(dirty) + 4
	budget := width - fixed
	if budget < 0 {
		budget = 0
	}

	// tagBudget reserves 1 column up front for the separator that will be
	// written before the tag whenever it ends up non-empty -- truncating
	// tagText against the full budget instead (and only then subtracting
	// the separator) let the separator push the whole row 1 column past
	// width, silently overflowing the pane exactly like the historical bug
	// this package's tests guard against.
	tagBudget := budget - 1
	if tagBudget < 0 {
		tagBudget = 0
	}
	tagTruncated := false
	if lipgloss.Width(tagText) > tagBudget {
		tagText = truncateEllipsis(tagText, tagBudget)
		tagTruncated = true
	}
	branchBudget := budget - lipgloss.Width(tagText)
	if tagText != "" {
		branchBudget-- // separator before the tag
	}
	if branchBudget < 0 {
		branchBudget = 0
	}
	branch := padField(w.Branch, branchBudget)

	var b strings.Builder
	written := 0
	writeField := func(text string, r role) {
		b.WriteString(styleFor(r, selected).Render(text))
		written += lipgloss.Width(text)
	}
	writeSep := func() {
		b.WriteString(styleFor(rolePlain, selected).Render(" "))
		written++
	}

	writeField(markerGlyph, markerRole)
	writeSep()
	writeField(name, roleFg)
	writeSep()
	writeField(branch, roleDim)
	writeSep()
	writeField(pr, prRole)
	writeSep()
	writeField(dirty, roleOrange)

	if tagText != "" {
		writeSep()
		if tagTruncated || len(tagParts) == 0 {
			writeField(tagText, roleDim)
		} else {
			for i, p := range tagParts {
				if i > 0 {
					writeSep()
				}
				writeField(p.text, p.role)
			}
		}
	}

	if pad := width - written; pad > 0 {
		b.WriteString(styleFor(rolePlain, selected).Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// rowMarker reports a worktree's liveness glyph and colour. Kind plays no
// part here -- a claude-managed/foreign worktree still shows its own real
// liveness; ownership is the separate "⚠ agent" tag below, not the marker.
func rowMarker(w model.Workspace) (string, role) {
	if w.HasLiveSession() || len(w.Procs) > 0 {
		return "●", roleGreen
	}
	return "○", roleDim
}

func displayName(w model.Workspace) string {
	name := filepath.Base(w.Path)
	if w.Kind == model.KindNested || w.Kind == model.KindClaudeManaged {
		name = filepath.Join(filepath.Base(filepath.Dir(w.Path)), name)
	}
	return name
}

// rowPR returns the PR column's text and colour. The text is exactly what
// it was before styling existed -- the PR number when open, "MERGED" when
// merged, "—" when there is none -- only the colour is new.
func rowPR(w model.Workspace) (string, role) {
	switch {
	case w.PR.Number != 0 && w.PR.State == "MERGED":
		return "MERGED", roleGreen
	case w.PR.Number != 0:
		return fmt.Sprintf("#%d", w.PR.Number), roleAmber
	default:
		return "—", roleDim
	}
}

// rowDirty must never render an unknown status the same as a clean one: a
// blank column already means "known clean", so a workspace whose git status
// couldn't be read (w.StatusKnown false -- the same signal PruneBlockers
// treats as a hard blocker, see model.PruneBlockers) gets its own glyph
// instead of falling through to that same blank.
func rowDirty(w model.Workspace) string {
	if !w.StatusKnown {
		return "?"
	}
	if w.DirtyCount == 0 {
		return ""
	}
	return fmt.Sprintf("%d✎", w.DirtyCount)
}

// rowTagParts is advisory only: it is rendered as plain styled text and
// nowhere wired to an action, so it can never trigger a deletion.
func rowTagParts(w model.Workspace) []tagPart {
	var parts []tagPart
	if w.Kind == model.KindClaudeManaged || w.Kind == model.KindForeign {
		parts = append(parts, tagPart{"⚠ agent", roleDim})
	}
	if w.FreeHint() {
		parts = append(parts, tagPart{"FREE", roleGreenBold})
	}
	return parts
}

func tagPlainText(parts []tagPart) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.text
	}
	return strings.Join(texts, " ")
}

// RenderSession formats one Claude Code session as an exact-width, styled
// line: a live/idle marker, then its label. It always goes through
// Session.Label(), never Title directly -- roughly half of real sessions
// have no ai-title, and reading Title alone would leave those rows blank.
func RenderSession(s model.Session, width int) string {
	if width <= 0 {
		return ""
	}
	marker, r := "○", roleDim
	if s.Live {
		marker, r = "●", roleGreen
	}
	labelWidth := width - lipgloss.Width(marker) - 1
	if labelWidth < 0 {
		labelWidth = 0
	}
	label := padField(s.Label(), labelWidth)
	return styleFor(r, false).Render(marker) +
		styleFor(rolePlain, false).Render(" ") +
		styleFor(roleFg, false).Render(label)
}

// RenderProc formats one non-Claude process as an exact-width, styled line.
func RenderProc(p model.Proc, width int) string {
	if width <= 0 {
		return ""
	}
	pid := fmt.Sprintf("%d", p.PID)
	fixed := lipgloss.Width(pid) + lipgloss.Width(p.Elapsed) + 2
	cmdWidth := width - fixed
	if cmdWidth < 0 {
		cmdWidth = 0
	}
	cmd := padField(p.Command, cmdWidth)

	var b strings.Builder
	written := 0
	write := func(text string, r role) {
		b.WriteString(styleFor(r, false).Render(text))
		written += lipgloss.Width(text)
	}
	write(pid, roleDim)
	write(" ", rolePlain)
	write(cmd, roleFg)
	write(" ", rolePlain)
	write(p.Elapsed, roleDim)

	if pad := width - written; pad > 0 {
		b.WriteString(styleFor(rolePlain, false).Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// padField pads s with trailing spaces to exactly width columns, or
// truncates it (via truncateEllipsis) when it is already wider. The result
// always measures exactly width via lipgloss.Width, for width > 0.
func padField(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	switch {
	case w > width:
		return truncateEllipsis(s, width)
	case w < width:
		return s + strings.Repeat(" ", width-w)
	default:
		return s
	}
}

// truncateEllipsis cuts s to at most width columns (per lipgloss.Width, so
// double-width runes are accounted for), always on a rune boundary --
// branch names and prompts contain non-ASCII -- appending "…" whenever it
// actually truncated. width <= 0 yields "".
func truncateEllipsis(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	w := 0
	for _, r := range runes {
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 { // leave room for the "…" itself
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}

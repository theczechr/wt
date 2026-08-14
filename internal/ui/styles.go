package ui

import "github.com/charmbracelet/lipgloss"

// Colour tokens. Every one is an AdaptiveColor so the palette works on both
// light and dark terminals -- lipgloss picks Light or Dark per-call based on
// the terminal's reported background. NO_COLOR is handled for free by
// lipgloss's default renderer: termenv.EnvColorProfile() (which the default
// renderer's ColorProfile() delegates to, see charmbracelet/lipgloss
// renderer.go) degrades to the Ascii profile whenever NO_COLOR is set,
// before it ever looks at the terminal, which turns every
// Foreground/Background call below into a no-op. Nothing here needs to
// special-case it.
var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#58a6ff"} // pane titles, repo headers, cursor
	colorFg     = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#e6edf3"} // worktree name, session label
	colorDim    = lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#8b949e"} // branch text, idle marker, "—", "agent"
	colorGreen  = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"} // MERGED, live marker, FREE
	colorAmber  = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#d29922"} // open PR number
	colorOrange = lipgloss.AdaptiveColor{Light: "#bc4c00", Dark: "#ff9147"} // dirty count
	colorBorder = lipgloss.AdaptiveColor{Light: "#d0d7de", Dark: "#30363d"} // pane borders
	colorSelBg  = lipgloss.AdaptiveColor{Light: "#d7e3fc", Dark: "#1c3a5f"} // selected row background
)

// role names a semantic colour/weight combination, not a raw colour --
// callers ask for "the PR-open colour" or "the dim colour", never a hex
// value, so the palette stays centralised here.
type role int

const (
	roleFg role = iota
	roleDim
	roleGreen
	roleGreenBold
	roleAmber
	roleOrange
	roleAccent
	roleAccentBold
	roleBorder
	rolePlain
)

// styleTable[role][0] is the unselected style, [role][1] is the same role
// with the selected-row background painted in. Built once at package init
// instead of constructed per call -- RenderRow alone issues on the order of
// ten Style.Render calls per worktree, and this is on the hot path measured
// by BenchmarkViewSeventyOneWorkspaces.
var styleTable = [...][2]lipgloss.Style{
	roleFg: {
		lipgloss.NewStyle().Foreground(colorFg),
		lipgloss.NewStyle().Foreground(colorFg).Background(colorSelBg),
	},
	roleDim: {
		lipgloss.NewStyle().Foreground(colorDim),
		lipgloss.NewStyle().Foreground(colorDim).Background(colorSelBg),
	},
	roleGreen: {
		lipgloss.NewStyle().Foreground(colorGreen),
		lipgloss.NewStyle().Foreground(colorGreen).Background(colorSelBg),
	},
	roleGreenBold: {
		lipgloss.NewStyle().Foreground(colorGreen).Bold(true),
		lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Background(colorSelBg),
	},
	roleAmber: {
		lipgloss.NewStyle().Foreground(colorAmber),
		lipgloss.NewStyle().Foreground(colorAmber).Background(colorSelBg),
	},
	roleOrange: {
		lipgloss.NewStyle().Foreground(colorOrange),
		lipgloss.NewStyle().Foreground(colorOrange).Background(colorSelBg),
	},
	roleAccent: {
		lipgloss.NewStyle().Foreground(colorAccent),
		lipgloss.NewStyle().Foreground(colorAccent).Background(colorSelBg),
	},
	roleAccentBold: {
		lipgloss.NewStyle().Foreground(colorAccent).Bold(true),
		lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Background(colorSelBg),
	},
	roleBorder: {
		lipgloss.NewStyle().Foreground(colorBorder),
		lipgloss.NewStyle().Foreground(colorBorder).Background(colorSelBg),
	},
	rolePlain: {
		lipgloss.NewStyle(),
		lipgloss.NewStyle().Background(colorSelBg),
	},
}

// styleFor returns the style for a role, with the selected-row background
// mixed in when selected is true. It is never given a Width -- lipgloss
// word-wraps content when Width() is set (see cellbuf.Wrap in
// charmbracelet/lipgloss's Style.Render), which is exactly the multi-line
// regression this package must not reintroduce. Every width decision here
// happens on plain text, before this is ever called; see render.go.
func styleFor(r role, selected bool) lipgloss.Style {
	if selected {
		return styleTable[r][1]
	}
	return styleTable[r][0]
}

// renderPane frames already-sized content in a rounded border with a title
// spliced into the top edge, e.g. "╭─ WORKTREES ─────╮". Every line handed
// back is exactly outerWidth columns wide (lipgloss.Width, border included)
// and there are exactly outerHeight lines.
//
// contentLines must already be individually sized to outerWidth-2 (via
// lipgloss.Width) by the caller -- RenderRow/RenderSession/RenderProc all
// guarantee this. This function only concatenates already-sized,
// already-styled pieces; it never calls Style.Width()/MaxWidth() on
// multi-line content itself, which is what would risk lipgloss wrapping an
// over-width line instead of the caller's own truncation ever being
// reached -- the exact historical bug this redesign exists to not repeat.
func renderPane(title string, contentLines []string, outerWidth, outerHeight int) string {
	inner := outerWidth - 2
	if inner < 0 {
		inner = 0
	}
	rows := outerHeight - 2
	if rows < 0 {
		rows = 0
	}

	var b []byte
	b = append(b, paneTopBorder(title, inner)...)
	b = append(b, '\n')

	blankInner := stylePlainBlank(inner)
	side := styleFor(roleBorder, false).Render("│")
	for i := 0; i < rows; i++ {
		b = append(b, side...)
		if i < len(contentLines) {
			b = append(b, contentLines[i]...)
		} else {
			b = append(b, blankInner...)
		}
		b = append(b, side...)
		b = append(b, '\n')
	}

	b = append(b, styleFor(roleBorder, false).Render("╰"+repeatRune('─', inner)+"╯")...)
	return string(b)
}

// stylePlainBlank returns width spaces, unstyled -- used to fill a pane's
// content area past the last real content line.
func stylePlainBlank(width int) string {
	if width <= 0 {
		return ""
	}
	return repeatRune(' ', width)
}

func repeatRune(r rune, n int) string {
	if n <= 0 {
		return ""
	}
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = r
	}
	return string(runes)
}

// paneTopBorder builds the "╭─ TITLE ────╮" line. All sizing happens on
// plain text (label, dash count) before either of the two pieces --
// border-coloured dashes/corners and the bold accent title -- is styled, and
// each is rendered independently rather than one nested inside the other,
// so neither's trailing reset code can clobber the other's colour.
func paneTopBorder(title string, inner int) string {
	label := " " + title + " "
	// The top line is "╭" + middle(inner cols) + "╮": the two corners sit
	// outside inner (matching the "│"-delimited content rows, whose visible
	// width is also inner), and middle is one leading dash, then label, then
	// however many trailing dashes fill the rest.
	maxLabel := inner - 1
	if maxLabel < 0 {
		maxLabel = 0
	}
	if lipgloss.Width(label) > maxLabel {
		label = truncateEllipsis(label, maxLabel)
	}
	dashes := inner - lipgloss.Width(label) - 1
	if dashes < 0 {
		dashes = 0
	}
	left := styleFor(roleBorder, false).Render("╭─")
	titleSeg := styleFor(roleAccentBold, false).Render(label)
	right := styleFor(roleBorder, false).Render(repeatRune('─', dashes) + "╮")
	return left + titleSeg + right
}

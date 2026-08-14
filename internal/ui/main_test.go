package ui

import (
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestMain forces the colour profile to Ascii for the whole package's test
// run, so every lipgloss.Style.Render call in this package becomes a no-op
// pass-through: no ANSI escape codes, styled or not. Every other test in
// this package (render_test.go, ui_test.go) asserts on plain-text content
// (strings.Contains for markers, PR numbers, dirty counts, ...), and this
// keeps those assertions checking real, unmodified content instead of
// requiring them to strip ANSI codes -- or, worse, being satisfied by a
// substring match that happens to survive embedded escape sequences by
// accident. TestRenderRowExactWidthWhenStyled (render_width_test.go)
// separately re-enables real colour to verify the ANSI-aware width
// invariant this package's styling relies on.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

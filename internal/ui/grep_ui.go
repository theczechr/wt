// Grep sessions: the bubbletea glue (debounce, generation-tagged streaming,
// key handling) and rendering for the picker opened by "<space> g". See
// grep_search.go for the actual rg process management and JSONL parsing.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/theczechr/wt/internal/model"
)

// grepDebounceDelay is how long the grep picker waits after the last
// keystroke before starting a search -- measured against this machine's
// ~28ms time-to-first-match, comfortably longer than a single keystroke's
// jitter but short enough that typing still feels responsive.
const grepDebounceDelay = 80 * time.Millisecond

// grepDebounceMsg fires grepDebounceDelay after a keystroke that changed
// the query. seq ties it to the keystroke that armed it -- any keystroke
// since then bumped grepState.seq, so an unequal seq means a newer
// keystroke already superseded this timer and it must be dropped rather
// than starting a search for a query the user has since changed.
type grepDebounceMsg struct{ seq int }

// grepState is the grep picker's state: the query and scroll cursor, the
// streamed-so-far results, and the machinery (gen, cancel, seq) that makes
// streaming safe under rapid retyping. Reset wholesale by newGrepState each
// time "<space> g" is opened.
type grepState struct {
	query  string
	cursor int

	hits    []grepHit
	scanned int  // transcript files the most recent completed/in-flight search has told us it searched so far
	capped  bool // len(hits) hit grepMaxResults; the footer notes it

	searching bool
	err       error // e.g. ErrRipgrepNotFound; set once at newGrepState and never cleared

	dirIndex map[string]model.Workspace // built once, forward, from the workspace list at picker-open time; see buildDirIndex

	gen    int                // current search generation; every started search bumps this, and every event is tagged with the generation that produced it
	cancel context.CancelFunc // cancels the in-flight search, if any
	ch     <-chan grepEvent   // the current generation's event stream, re-read by handleGrepEvent after every event that still belongs to gen
	seq    int                // debounce sequence; bumped on every keystroke that changes the query
}

// newGrepState resets the picker for a fresh "<space> g" open: builds the
// forward dir->workspace index from the current workspace list and checks
// once whether rg is even on PATH.
func newGrepState(ws []model.Workspace) grepState {
	g := grepState{dirIndex: buildDirIndex(ws)}
	if _, err := lookRipgrep(); err != nil {
		g.err = ErrRipgrepNotFound
	}
	return g
}

// selected returns the hit under the cursor, if any.
func (g grepState) selected() (grepHit, bool) {
	if g.cursor < 0 || g.cursor >= len(g.hits) {
		return grepHit{}, false
	}
	return g.hits[g.cursor], true
}

// updateGrep handles keys while the grep picker owns input. Arrow keys and
// Ctrl-N/Ctrl-P navigate (not j/k -- see find.go's updateFind for why);
// every other printable key is literal query text.
func (m uiModel) updateGrep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.stopGrepSearch()
		return m, tea.Quit
	case tea.KeyEsc:
		m.stopGrepSearch()
		m.picker = pickerNone
		return m, nil
	case tea.KeyEnter:
		if h, ok := m.grep.selected(); ok && !h.Untracked {
			m.chosen = h.Workspace.Path
			m.session = h.SessionID
			m.action = ActionResume
			m.stopGrepSearch()
			m.picker = pickerNone
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyTab:
		if h, ok := m.grep.selected(); ok && !h.Untracked {
			m.chosen = h.Workspace.Path
			m.action = ActionCd
			m.stopGrepSearch()
			m.picker = pickerNone
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		if n := len(m.grep.hits); m.grep.cursor < n-1 {
			m.grep.cursor++
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		if m.grep.cursor > 0 {
			m.grep.cursor--
		}
		return m, nil
	case tea.KeyBackspace:
		if m.grep.query != "" {
			_, size := utf8.DecodeLastRuneInString(m.grep.query)
			m.grep.query = m.grep.query[:len(m.grep.query)-size]
		}
		return m.restartGrepDebounce()
	case tea.KeySpace:
		m.grep.query += " "
		return m.restartGrepDebounce()
	case tea.KeyRunes:
		m.grep.query += string(msg.Runes)
		return m.restartGrepDebounce()
	}
	return m, nil
}

// stopGrepSearch cancels any in-flight search. Called from every key that
// leaves the grep picker (Esc, Enter, Tab, Ctrl-C); Run() additionally
// cancels on the way out regardless of which path was taken, so this is
// belt-and-braces against a leaked rg process, not the only guard.
func (m *uiModel) stopGrepSearch() {
	if m.grep.cancel != nil {
		m.grep.cancel()
		m.grep.cancel = nil
	}
}

// restartGrepDebounce records a query-changing keystroke: it bumps seq
// (invalidating any timer already in flight), clears the stale
// previous-query results immediately for responsiveness, and arms a new
// grepDebounceDelay timer. It does NOT cancel a currently-running rg here --
// that happens in startGrepSearch, right before the next one is spawned, so
// a search that's about to complete anyway is not killed and restarted for
// nothing if the debounce window closes before it finishes.
func (m uiModel) restartGrepDebounce() (tea.Model, tea.Cmd) {
	m.grep.seq++
	m.grep.cursor = 0
	m.grep.hits = nil
	m.grep.scanned = 0
	m.grep.capped = false
	if m.grep.query == "" || m.grep.err != nil {
		m.stopGrepSearch()
		m.grep.searching = false
		return m, nil
	}
	seq := m.grep.seq
	return m, tea.Tick(grepDebounceDelay, func(time.Time) tea.Msg {
		return grepDebounceMsg{seq: seq}
	})
}

// startGrepSearch is called once a debounce timer actually fires for the
// current query. It cancels any still-running previous search (the one
// point search generations are torn down), bumps the generation, and kicks
// off streaming.
func (m uiModel) startGrepSearch() (tea.Model, tea.Cmd) {
	m.stopGrepSearch()
	if m.grep.query == "" || m.grep.err != nil {
		m.grep.searching = false
		return m, nil
	}
	m.grep.gen++
	gen := m.grep.gen
	ctx, cancel := context.WithCancel(context.Background())
	m.grep.cancel = cancel
	m.grep.searching = true
	ch := runGrepSearch(ctx, gen, m.grep.query, m.grep.dirIndex)
	m.grep.ch = ch
	return m, listenGrepEvent(ch)
}

// listenGrepEvent reads exactly one event off ch and returns it as a
// tea.Msg. handleGrepEvent re-issues this after every event that still
// belongs to the current generation, forming the read loop bubbletea's
// architecture expects for a streaming source: one message in, one command
// out, for as long as there's more to read.
func listenGrepEvent(ch <-chan grepEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return ev
	}
}

// handleGrepEvent applies one streamed grepEvent. Events tagged with a
// generation other than the current one are dropped without being
// requeued: that channel belongs to a search already superseded by a newer
// keystroke, whose context is already cancelled, so its producer goroutine
// is already unwinding (or will as soon as it next tries to send) and
// needs no further draining from here.
func (m uiModel) handleGrepEvent(ev grepEvent) (tea.Model, tea.Cmd) {
	if ev.gen != m.grep.gen {
		return m, nil
	}
	switch {
	case ev.hit != nil:
		if len(m.grep.hits) < grepMaxResults {
			m.grep.hits = append(m.grep.hits, *ev.hit)
		} else {
			m.grep.capped = true
		}
		return m, listenGrepEvent(m.grep.ch)
	case ev.done != nil:
		m.grep.searching = false
		m.grep.scanned = ev.done.scanned
		if ev.done.err != nil {
			m.grep.err = ev.done.err
		}
		return m, nil
	}
	return m, nil
}

// Rendering. Each hit renders as two screen lines: a metadata header
// (marker, worktree name, session label, relative time, PR) and an indented
// excerpt with the query highlighted and surrounding context elided. Both
// are windowed in hit-sized pairs (grepVisibleWindow), not by raw line
// count, so scrolling can never split a hit's header from its own excerpt.

const (
	grepNameWidth    = 22
	grepTimeWidth    = 9
	grepPRWidth      = 7
	grepContextRunes = 20 // runes of context shown on each side of the match, before that side's own budget clamp
)

// grepVisibleWindow picks a [start,end) slice of hit indices that keeps
// cursor on screen within a budget of maxRows screen lines -- the
// hit-indexed analogue of visibleWindow, needed because every hit here
// takes exactly 2 lines rather than 1.
func grepVisibleWindow(hitCount, cursor, maxRows int) (start, end int) {
	maxHits := maxRows / 2
	if maxHits < 1 {
		maxHits = 1
	}
	if hitCount <= maxHits {
		return 0, hitCount
	}
	start = cursor - maxHits/2
	if start < 0 {
		start = 0
	}
	end = start + maxHits
	if end > hitCount {
		end = hitCount
		start = end - maxHits
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

// renderGrepHitHeader renders one hit's metadata row: liveness marker,
// worktree name (or "(untracked)"), session label, relative timestamp, and
// PR number when present -- the same information the dashboard's own
// worktree/session rows surface, laid out for a single line.
func renderGrepHitHeader(h grepHit, selected bool, width int) string {
	marker, markerRole := "?", roleDim
	name := "(untracked)"
	label := "(untracked session)"
	if !h.Untracked {
		name = displayName(h.Workspace)
		label = h.Session.Label()
		if h.Session.Live {
			marker, markerRole = "●", roleGreen
		} else {
			marker, markerRole = "○", roleDim
		}
	}
	name = padField(name, grepNameWidth)
	relT := padField(relTime(h.Timestamp), grepTimeWidth)

	prText, prRole := "—", roleDim
	if !h.Untracked && h.Session.PRNumber != 0 {
		prText, prRole = fmt.Sprintf("#%d", h.Session.PRNumber), roleAmber
	}
	pr := padField(prText, grepPRWidth)

	fixed := lipgloss.Width(marker) + lipgloss.Width(name) + lipgloss.Width(relT) + lipgloss.Width(pr) + 3
	labelBudget := width - fixed
	if labelBudget < 0 {
		labelBudget = 0
	}
	label = padField(label, labelBudget)

	var b strings.Builder
	write := func(text string, r role) { b.WriteString(styleFor(r, selected).Render(text)) }
	write(marker, markerRole)
	write(" ", rolePlain)
	write(name, roleFg)
	write(" ", rolePlain)
	write(label, roleFg)
	write(" ", rolePlain)
	write(relT, roleDim)
	write(pr, prRole)

	written := lipgloss.Width(marker) + 1 + lipgloss.Width(name) + 1 + lipgloss.Width(label) + 1 + lipgloss.Width(relT) + lipgloss.Width(pr)
	if pad := width - written; pad > 0 {
		b.WriteString(styleFor(rolePlain, selected).Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// renderGrepHitExcerpt renders one hit's matched-text row: an indented,
// width-exact line with the surrounding context elided ("…") and the query
// itself highlighted. All truncation happens on plain text (elideAroundMatch)
// before any of the three pieces is ever styled, exactly like every other
// row in this package -- see render.go's package doc comment.
func renderGrepHitExcerpt(h grepHit, width int) string {
	const indent = "    "
	budget := width - lipgloss.Width(indent)
	if budget < 0 {
		budget = 0
	}
	before, match, after := elideAroundMatch(h.Text, h.MatchStart, h.MatchEnd, budget)
	used := lipgloss.Width(before) + lipgloss.Width(match) + lipgloss.Width(after)
	pad := budget - used
	if pad < 0 {
		pad = 0
	}
	return styleFor(rolePlain, false).Render(indent) +
		styleFor(roleDim, false).Render(before) +
		styleFor(roleAccentBold, false).Render(match) +
		styleFor(roleDim, false).Render(after) +
		styleFor(rolePlain, false).Render(strings.Repeat(" ", pad))
}

// elideAroundMatch splits text's [start,end) rune range (the query match)
// out from its surrounding context, trimming each side to fit within
// budget total columns: up to grepContextRunes of context per side,
// further clamped so before+match+after never exceeds budget. before is
// trimmed from its head ("…" prefix when trimmed); after is trimmed from
// its tail ("…" suffix when trimmed) -- both via lipgloss.Width, so the
// three pieces concatenated are always <= budget columns, leaving the
// caller free to pad the remainder with plain spaces.
func elideAroundMatch(text string, start, end, budget int) (before, match, after string) {
	runes := []rune(text)
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start > end {
		start, end = end, start
	}
	match = string(runes[start:end])
	matchW := lipgloss.Width(match)
	if matchW >= budget {
		return "", truncateEllipsis(match, budget), ""
	}

	remaining := budget - matchW
	sideBudget := grepContextRunes
	if sideBudget*2 > remaining {
		sideBudget = remaining / 2
	}
	before = tailElide(string(runes[:start]), sideBudget)
	after = truncateEllipsis(string(runes[end:]), remaining-sideBudget)
	return before, match, after
}

// tailElide keeps the LAST budget columns of s -- the mirror of
// truncateEllipsis, which keeps the first -- prefixing "…" when it actually
// cut something off. Always cuts on a rune boundary, per lipgloss.Width.
func tailElide(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= budget {
		return s
	}
	if budget == 1 {
		return "…"
	}
	runes := []rune(s)
	w, i := 0, len(runes)
	for i > 0 {
		rw := lipgloss.Width(string(runes[i-1]))
		if w+rw > budget-1 { // leave room for the leading "…"
			break
		}
		w += rw
		i--
	}
	return "…" + string(runes[i:])
}

// relTime formats t as a short relative timestamp ("2h ago"), or "—" for
// the zero value (no parseable timestamp on the record and no known
// session to fall back to).
func relTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
}

// grepFooterCounts renders the picker's summary line, e.g.
// "2 sessions · searched 274 transcripts". Sessions counts distinct
// SessionIDs among hits, not raw hit count, since one session can have
// several matching prompts. searching is true whenever the current
// generation hasn't reached its "done" event yet, in which case scanned is
// only a lower bound (rg hasn't reported its final per-file count yet) --
// shown as "searching…" instead of a misleadingly low "searched 0
// transcripts" the instant results start streaming in.
func grepFooterCounts(hits []grepHit, scanned int, capped, searching bool) string {
	sessions := map[string]bool{}
	for _, h := range hits {
		sessions[h.SessionID] = true
	}
	s := fmt.Sprintf("%d session", len(sessions))
	if len(sessions) != 1 {
		s += "s"
	}
	s += " · "
	if searching {
		s += "searching…"
	} else {
		s += fmt.Sprintf("searched %d transcript", scanned)
		if scanned != 1 {
			s += "s"
		}
	}
	if capped {
		s += " (capped)"
	}
	return s
}

// viewGrepPicker renders the full-screen grep overlay: the query input,
// then either an error/status line or the streamed, scrolled hit list, with
// a footer summarising counts and key hints.
func (m uiModel) viewGrepPicker(width, outerHeight int) string {
	inner := width - 2
	if inner < 0 {
		inner = 0
	}

	if m.grep.err != nil {
		content := []string{styleFor(roleOrange, false).Render(padField("  "+m.grep.err.Error(), inner))}
		pane := renderPane("GREP", content, width, outerHeight)
		footer := styleFor(roleDim, false).Render("esc cancel")
		return pane + "\n\n" + footer + "\n"
	}

	rows := outerHeight - 2 - 1 // borders, then the input line
	if rows < 2 {
		rows = 2
	}
	hs, he := grepVisibleWindow(len(m.grep.hits), m.grep.cursor, rows)

	content := make([]string, 0, rows+1)
	content = append(content, renderPickerInput("Grep:", m.grep.query, inner))
	switch {
	case m.grep.query == "":
		content = append(content, styleFor(roleDim, false).Render(padField("  type to search your prompts and session titles", inner)))
	case len(m.grep.hits) == 0 && m.grep.searching:
		content = append(content, styleFor(roleDim, false).Render(padField("  searching…", inner)))
	case len(m.grep.hits) == 0:
		content = append(content, styleFor(roleDim, false).Render(padField("  (no matches)", inner)))
	default:
		for i := hs; i < he; i++ {
			h := m.grep.hits[i]
			selected := i == m.grep.cursor
			prefix := "  "
			prefixRole := rolePlain
			if selected {
				prefix = "❯ "
				prefixRole = roleAccentBold
			}
			rowWidth := inner - lipgloss.Width(prefix)
			if rowWidth < 0 {
				rowWidth = 0
			}
			content = append(content,
				styleFor(prefixRole, selected).Render(prefix)+renderGrepHitHeader(h, selected, rowWidth),
				renderGrepHitExcerpt(h, inner),
			)
		}
	}

	pane := renderPane("GREP", content, width, outerHeight)
	footer := styleFor(roleDim, false).Render(grepFooterCounts(m.grep.hits, m.grep.scanned, m.grep.capped, m.grep.searching))
	if m.grep.searching {
		footer += "  " + styleFor(roleAccentBold, false).Render("⟳")
	}
	footer += "   " + styleFor(roleDim, false).Render("↑/↓ ^n/^p move  ⏎ resume  ⇥ cd  esc cancel")
	return pane + "\n\n" + footer + "\n"
}

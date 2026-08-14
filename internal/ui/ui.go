package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/trash"
)

// Action is what the user chose on exit.
type Action int

const (
	ActionNone Action = iota
	ActionCd
	ActionResume
)

// pickerMode is which additive which-key overlay (if any) currently owns
// key input. Zero value (pickerNone) is the ordinary dashboard -- exactly
// today's behaviour, so every existing test that constructs a bare
// uiModel{} keeps working unchanged.
type pickerMode int

const (
	pickerNone pickerMode = iota
	pickerWhichKey
	pickerFind
	pickerGrep
	pickerConfirmDelete
	pickerTrash
)

// screenLine is one rendered line of the worktree pane. wsIndex is the
// index into the current visible() slice this line represents, or -1 for a
// repo header / spacer line that the cursor can never land on.
type screenLine struct {
	text    string
	wsIndex int
}

type uiModel struct {
	ws        []model.Workspace // master list, pre-sorted by Run
	cursor    int               // index into visible()
	filter    string
	filtering bool
	width     int
	height    int
	action    Action
	chosen    string
	session   string
	status    string // ephemeral info line, e.g. "can't attach to a live session"

	// refresh re-collects the whole workspace list, plus a count of
	// processes whose cwd could not be resolved during that collection
	// (see discover.Processes.UnresolvedCwds and aggregate.Collect); nil
	// disables both the automatic startup refresh and the 'R' key.
	// refreshing is true while a refresh triggered by either of those is in
	// flight, and drives the "⟳" indicator so a stale (e.g.
	// snapshot-painted) view is never mistaken for a current one.
	refresh    func() ([]model.Workspace, int)
	refreshing bool

	// unresolvedProcs is the most recently known count of processes whose
	// cwd could not be resolved -- 0 until the first Collect/refresh lands,
	// same as every other field a snapshot-painted first frame doesn't yet
	// know. Read by unresolvedProcsWarning (delete_confirm.go): this is a
	// fact about the whole process table, not about confirmTarget alone, so
	// unlike confirmUnpushedCount below it is never re-fetched per arm --
	// it just tracks whatever the last successful process snapshot found.
	unresolvedProcs int

	// picker is which additive overlay (which-key popup, find, grep) owns
	// input right now; pickerNone means the ordinary dashboard above.
	picker pickerMode

	// find picker: fuzzy-matches the in-memory ws list, no I/O.
	findQuery  string
	findCursor int

	// grep picker: streams matches from ripgrep over Claude transcripts.
	grep grepState

	// confirm-delete: armed by "d" from the dashboard, confirmed by a
	// second "d" within confirmWindow, cancelled by anything else. See
	// delete_confirm.go.
	confirmTarget  model.Workspace
	confirmArmedAt time.Time
	deleting       bool // true while an armed delete's git command is in flight

	// confirmUnpushedCount/-Loaded carry the lazily-fetched "commits exist
	// only here" count for confirmTarget, when it has no upstream. Fetched
	// by exactly one discover.LocalOnlyCommitCount call, fired only when
	// "d" arms the confirmation (never during a refresh/Collect, which
	// would add one git fork per worktree to that path) and only for the
	// single armed target. confirmUnpushedCountLoaded is false both before
	// the result lands and if the git command failed -- either way
	// viewConfirmDelete degrades to unqualified wording rather than
	// showing a stale or wrong number. See delete_confirm.go.
	confirmUnpushedCount       int
	confirmUnpushedCountLoaded bool

	// trash: <space>t lists what soft deletes have recorded. trashMode is
	// display-only (what a fresh "d d" would do); the actual behaviour is
	// entirely owned by del, injected from main via TrashOps.
	trash       []trash.Entry
	trashCursor int
	trashMode   string // "soft" or "hard" -- see deleteModeLabel
	trashBusy   bool   // true while a restore/purge is in flight

	del     DeleteFunc
	restore RestoreFunc
	purge   PurgeFunc
}

// DeleteFunc performs one worktree deletion -- soft or hard, entirely
// chosen by the caller's configured mode, not by the UI. entry is non-nil
// exactly when a soft delete recorded a new trash manifest entry; info is a
// message to show even on success (e.g. a hard delete's "branch kept"
// notice); err is nil exactly when the worktree itself was removed (a kept
// branch on hard delete is reported via info, not err -- see
// trash.HardDeleteResult).
type DeleteFunc func(ws model.Workspace) (entry *trash.Entry, info string, err error)

// RestoreFunc restores one trashed entry (trash.Restore, injected from
// main with the right config.Repo already resolved).
type RestoreFunc func(e trash.Entry) error

// PurgeFunc drops one trash manifest entry without touching disk
// (trash.Remove).
type PurgeFunc func(e trash.Entry) error

// TrashOps bundles the trash view's initial data and the callbacks that
// perform its destructive operations (and the main dashboard's "d d" flow,
// which shares Delete), so Run's signature doesn't balloon as this feature
// grows.
type TrashOps struct {
	Entries []trash.Entry
	Mode    string // "soft" or "hard" -- display only, see deleteModeLabel
	Delete  DeleteFunc
	Restore RestoreFunc
	Purge   PurgeFunc
}

// refreshedMsg carries a completed background Collect back into Update.
type refreshedMsg struct {
	ws              []model.Workspace
	unresolvedProcs int
}

// deleteDoneMsg carries a completed SoftDelete/HardDelete back into Update.
type deleteDoneMsg struct {
	path  string
	entry *trash.Entry
	info  string
	err   error
}

// restoreDoneMsg carries a completed Restore back into Update.
type restoreDoneMsg struct {
	entry trash.Entry
	err   error
}

// purgeDoneMsg carries a completed manifest-only purge back into Update.
type purgeDoneMsg struct {
	entry trash.Entry
	err   error
}

// unpushedCountMsg carries a completed discover.LocalOnlyCommitCount call
// back into Update, for the delete confirmation's no-upstream warning. path
// guards applyUnpushedCount against a stale result landing after the user
// already cancelled or re-armed on a different target -- see its comment.
type unpushedCountMsg struct {
	path  string
	count int
	ok    bool
}

// visible returns the workspaces matching the current filter. The filter
// matches against path, branch, repo, and every session's Label() -- never
// Session.Title directly, since roughly half of real sessions have no
// ai-title and Label() is what actually appears in the sessions pane.
func (m uiModel) visible() []model.Workspace {
	if m.filter == "" {
		return m.ws
	}
	var out []model.Workspace
	q := strings.ToLower(m.filter)
	for _, w := range m.ws {
		hay := strings.ToLower(w.Path + " " + w.Branch + " " + w.Repo)
		for _, s := range w.Sessions {
			hay += " " + strings.ToLower(s.Label())
		}
		if strings.Contains(hay, q) {
			out = append(out, w)
		}
	}
	return out
}

// Init kicks off a background refresh only when the model was constructed
// already showing one as in flight -- i.e. Run painted from a persisted
// snapshot rather than a fresh Collect, so the real data needs to land as
// soon as possible after the first frame.
func (m uiModel) Init() tea.Cmd {
	if m.refreshing && m.refresh != nil {
		return m.refreshCmd()
	}
	return nil
}

// refreshCmd runs m.refresh on bubbletea's own goroutine and reports the
// result back as a refreshedMsg; it never blocks the UI loop.
func (m uiModel) refreshCmd() tea.Cmd {
	refresh := m.refresh
	return func() tea.Msg {
		ws, unresolvedProcs := refresh()
		return refreshedMsg{ws: ws, unresolvedProcs: unresolvedProcs}
	}
}

func (m uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case refreshedMsg:
		return m.applyRefresh(msg.ws, msg.unresolvedProcs), nil
	case deleteDoneMsg:
		return m.applyDeleteResult(msg)
	case restoreDoneMsg:
		return m.applyRestoreResult(msg)
	case purgeDoneMsg:
		return m.applyPurgeResult(msg)
	case unpushedCountMsg:
		return m.applyUnpushedCount(msg), nil
	case grepEvent:
		return m.handleGrepEvent(msg)
	case grepDebounceMsg:
		if msg.seq != m.grep.seq {
			// A later keystroke already superseded this timer; firing a
			// search for the query it was armed with would show stale
			// results for a heartbeat before the real one lands.
			return m, nil
		}
		return m.startGrepSearch()
	case tea.KeyMsg:
		switch {
		case m.filtering:
			return m.updateFiltering(msg)
		case m.picker == pickerWhichKey:
			return m.updateWhichKey(msg)
		case m.picker == pickerFind:
			return m.updateFind(msg)
		case m.picker == pickerGrep:
			return m.updateGrep(msg)
		case m.picker == pickerConfirmDelete:
			return m.updateConfirmDelete(msg)
		case m.picker == pickerTrash:
			return m.updateTrash(msg)
		default:
			return m.updateNormal(msg)
		}
	}
	return m, nil
}

// applyRefresh swaps in freshly-collected data. It re-sorts (the filter
// itself needs no reapplication -- visible() re-derives it from m.filter on
// every render) and keeps the cursor on the same workspace by PATH rather
// than index, since the refreshed set can reorder, gain, or lose entries
// and an index would otherwise land the cursor on an unrelated row.
func (m uiModel) applyRefresh(ws []model.Workspace, unresolvedProcs int) uiModel {
	var selectedPath string
	if vis := m.visible(); len(vis) > 0 && m.cursor >= 0 && m.cursor < len(vis) {
		selectedPath = vis[m.cursor].Path
	}

	m.ws = sortWorkspaces(ws)
	m.unresolvedProcs = unresolvedProcs
	m.refreshing = false

	m.cursor = 0
	if selectedPath != "" {
		for i, w := range m.visible() {
			if w.Path == selectedPath {
				m.cursor = i
				break
			}
		}
	}
	return m
}

func (m uiModel) updateFiltering(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		// bubbletea does not quit on Ctrl-C by default; without this case
		// it's swallowed as an unrecognised filter keystroke and the only
		// way out of the filter is Esc.
		return m, tea.Quit
	case tea.KeyEnter, tea.KeyEsc:
		m.filtering = false
	case tea.KeyBackspace:
		if m.filter != "" {
			_, size := utf8.DecodeLastRuneInString(m.filter)
			m.filter = m.filter[:len(m.filter)-size]
		}
	case tea.KeySpace:
		m.filter += " "
	case tea.KeyRunes:
		m.filter += string(msg.Runes)
	}
	m.cursor = 0
	return m, nil
}

func (m uiModel) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := m.visible()
	m.status = ""
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(vis)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "/":
		m.filtering = true
		m.filter = ""
		m.cursor = 0
	case " ":
		m.picker = pickerWhichKey
	case "enter":
		if len(vis) > 0 {
			m.chosen = vis[m.cursor].Path
			m.action = ActionCd
			return m, tea.Quit
		}
	case "r":
		return m.updateResume(vis)
	case "d":
		if m.deleting || len(vis) == 0 {
			return m, nil
		}
		m.picker = pickerConfirmDelete
		m.confirmTarget = vis[m.cursor]
		m.confirmArmedAt = time.Now()
		// Reset any count left over from a previous arm -- otherwise a
		// fast re-arm on a different no-upstream target would flash the
		// old target's number for a frame before the new fetch lands.
		m.confirmUnpushedCount = 0
		m.confirmUnpushedCountLoaded = false
		if !m.confirmTarget.HasUpstream {
			return m, unpushedCountCmd(m.confirmTarget.Path)
		}
		return m, nil
	case "R":
		if m.refresh == nil || m.refreshing {
			return m, nil
		}
		m.refreshing = true
		return m, m.refreshCmd()
	case "]":
		m.jumpSection(vis, 1)
	case "[":
		m.jumpSection(vis, -1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		n := int(msg.String()[0] - '0')
		if starts := sectionStarts(vis); n <= len(starts) {
			m.cursor = starts[n-1]
		}
	}
	return m, nil
}

// sectionStarts returns, for each repo section in vis (in order), the index
// into vis of its first workspace. A section is a maximal run of
// consecutive entries sharing the same Repo -- exactly the grouping
// buildWorktreeLines renders a header for, so this is the single source of
// truth both the header numbering and the "]"/"["/1-9 jump keys key off of:
// they can never disagree about what section N means.
func sectionStarts(vis []model.Workspace) []int {
	var starts []int
	lastRepo := ""
	for i, w := range vis {
		if i == 0 || w.Repo != lastRepo {
			starts = append(starts, i)
			lastRepo = w.Repo
		}
	}
	return starts
}

// jumpSection moves the cursor to the first workspace of the next (dir=1)
// or previous (dir=-1) repo section, wrapping around at either end. With
// zero or one section it is a no-op -- there is nowhere else to jump to, and
// wrapping "around" to the same single section would otherwise snap the
// cursor to its top even when the user is already sitting in the middle of
// it.
func (m *uiModel) jumpSection(vis []model.Workspace, dir int) {
	starts := sectionStarts(vis)
	if len(starts) <= 1 {
		return
	}
	cur := 0
	for i, s := range starts {
		if s <= m.cursor {
			cur = i
		}
	}
	next := (cur + dir + len(starts)) % len(starts)
	m.cursor = starts[next]
}

// updateResume handles the "r" key. An idle session resumes normally; a
// live session cannot be attached to (there is no tmux underneath it), so
// its pid and path are surfaced as a status message instead of pretending
// the resume happened. The newest idle session is preferred when several
// exist.
func (m uiModel) updateResume(vis []model.Workspace) (tea.Model, tea.Cmd) {
	if len(vis) == 0 {
		return m, nil
	}
	w := vis[m.cursor]

	var newestIdle *model.Session
	var liveSession *model.Session
	for i := range w.Sessions {
		s := &w.Sessions[i]
		if s.Live {
			if liveSession == nil {
				liveSession = s
			}
			continue
		}
		if newestIdle == nil || s.Mtime.After(newestIdle.Mtime) {
			newestIdle = s
		}
	}

	switch {
	case newestIdle != nil:
		m.chosen = w.Path
		m.session = newestIdle.ID
		m.action = ActionResume
		return m, tea.Quit
	case liveSession != nil:
		m.status = fmt.Sprintf("session %s is live (pid %d) at %s — can't attach, no tmux underneath it",
			liveSession.Label(), liveSession.PID, w.Path)
	default:
		m.status = "no sessions in this worktree"
	}
	return m, nil
}

// narrowWidth is the terminal width below which the right-hand SESSIONS /
// PROCESSES panes are dropped rather than crushed: below it there is not
// enough room for a third column of anything readable, and the worktree
// list -- which is what the user actually opens this for -- gets the full
// width instead.
const narrowWidth = 100

func (m uiModel) View() string {
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	// Reserves the blank separator line and the footer line below the
	// boxed panes.
	paneHeight := height - 3
	if paneHeight < 6 {
		paneHeight = 20
	}

	// Pickers are additive overlays: while one is open it owns the whole
	// screen (there is no compositing layer to blend a floating box over
	// the dashboard's already-styled ANSI output), but none of them touch
	// m.ws, m.cursor, m.filter or the sort order below -- Esc always
	// returns to the exact dashboard state it left.
	switch m.picker {
	case pickerWhichKey:
		return m.viewWhichKey(width, height)
	case pickerFind:
		return m.viewFindPicker(width, paneHeight)
	case pickerGrep:
		return m.viewGrepPicker(width, paneHeight)
	case pickerConfirmDelete:
		return m.viewConfirmDelete(width, height)
	case pickerTrash:
		return m.viewTrashPicker(width, paneHeight)
	}

	vis := m.visible()
	if len(vis) > 0 && m.cursor >= len(vis) {
		m.cursor = len(vis) - 1
	}

	var body string
	if width < narrowWidth {
		body = m.renderWorktreePane(vis, width, paneHeight)
	} else {
		leftWidth := width - width/3
		if leftWidth < 20 {
			leftWidth = width
		}
		rightWidth := width - leftWidth
		if rightWidth < 10 {
			rightWidth = 10
			leftWidth = width - rightWidth
		}

		left := m.renderWorktreePane(vis, leftWidth, paneHeight)

		sessionsHeight := paneHeight / 2
		procsHeight := paneHeight - sessionsHeight
		right := lipgloss.JoinVertical(lipgloss.Left,
			m.renderSessionsPane(vis, rightWidth, sessionsHeight),
			m.renderProcsPane(vis, rightWidth, procsHeight),
		)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}

	var footer string
	switch {
	case m.filtering:
		footer = styleFor(roleFg, false).Render("/"+m.filter) + styleFor(roleAccentBold, false).Render("█")
	case m.status != "":
		footer = styleFor(roleOrange, false).Render(m.status)
	default:
		footer = styleFor(roleDim, false).Render("j/k move  [ ] section  1-9 repo  ⏎ cd  r resume  / filter  R refresh  q quit")
	}
	// Shown regardless of mode, so a refresh in flight while filtering (or
	// while a status line is up) is never hidden -- a stale view must never
	// be mistaken for a current one.
	if m.refreshing {
		footer += "  " + styleFor(roleAccentBold, false).Render("⟳")
	}
	return body + "\n\n" + footer + "\n"
}

// renderWorktreePane renders the repo-grouped, cursor-scrolled left column
// inside a bordered, titled box exactly outerWidth x outerHeight.
func (m uiModel) renderWorktreePane(vis []model.Workspace, outerWidth, outerHeight int) string {
	inner := outerWidth - 2
	if inner < 0 {
		inner = 0
	}
	lines := buildWorktreeLines(vis, m.cursor, inner)

	rows := outerHeight - 2
	if rows < 1 {
		rows = 1
	}
	start, end := visibleWindow(lines, m.cursor, rows)

	content := make([]string, 0, rows)
	for _, l := range lines[start:end] {
		content = append(content, l.text)
	}
	if len(vis) == 0 {
		content = append(content, styleFor(roleDim, false).Render(padField("  (no worktrees match)", inner)))
	}
	return renderPane("WORKTREES", content, outerWidth, outerHeight)
}

// buildWorktreeLines flattens the repo-grouped list into screen lines, each
// exactly `width` columns wide and tagged with the index into vis it
// represents (or -1 for a repo-header/spacer line the cursor can never land
// on). The cursor row gets a "❯ " prefix and the selected-row background;
// every other row gets two spaces of the same (unstyled) width instead, so
// the text columns always start at the same offset.
func buildWorktreeLines(vis []model.Workspace, cursor, width int) []screenLine {
	counts := map[string]int{}
	for _, w := range vis {
		counts[w.Repo]++
	}
	// sectionNum maps a vis index that starts a section to its 1-based
	// section number, straight from sectionStarts -- the same function the
	// "]"/"["/1-9 jump keys use, so a header's number always matches what
	// those keys actually jump to.
	sectionNum := make(map[int]int)
	for si, idx := range sectionStarts(vis) {
		sectionNum[idx] = si + 1
	}

	var lines []screenLine
	for i, w := range vis {
		if n, ok := sectionNum[i]; ok {
			lines = append(lines, screenLine{
				text:    renderRepoHeader(w.Repo, counts[w.Repo], n, width),
				wsIndex: -1,
			})
		}

		selected := i == cursor
		prefix := "  "
		prefixRole := rolePlain
		if selected {
			prefix = "❯ "
			prefixRole = roleAccentBold
		}
		rowWidth := width - lipgloss.Width(prefix)
		if rowWidth < 0 {
			rowWidth = 0
		}
		text := styleFor(prefixRole, selected).Render(prefix) + RenderRow(w, rowWidth, selected)
		lines = append(lines, screenLine{text: text, wsIndex: i})
	}
	return lines
}

// renderRepoHeader renders one "▾ [N ]repo            (N)" line: the repo
// name bold and accented, left-justified, with its worktree count
// right-aligned at the far edge of the pane. section is the header's 1-based
// jump-key number; sections beyond 9 (section outside 1..9) render with no
// number at all, since only the first 9 sections get a "1"-"9" key bound to
// them.
func renderRepoHeader(repo string, count, section, width int) string {
	left := fmt.Sprintf("▾ %s", repo)
	if section >= 1 && section <= 9 {
		left = fmt.Sprintf("▾ %d %s", section, repo)
	}
	right := fmt.Sprintf("(%d)", count)
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		return styleFor(roleAccentBold, false).Render(padField(left+" "+right, width))
	}
	return styleFor(roleAccentBold, false).Render(left) +
		styleFor(rolePlain, false).Render(strings.Repeat(" ", pad)) +
		styleFor(roleAccentBold, false).Render(right)
}

// visibleWindow picks a [start,end) slice of lines that keeps the line for
// cursorWs (the workspace index under the cursor) on screen, within maxRows.
func visibleWindow(lines []screenLine, cursorWs, maxRows int) (start, end int) {
	if maxRows <= 0 || len(lines) <= maxRows {
		return 0, len(lines)
	}
	cursorLine := 0
	for i, l := range lines {
		if l.wsIndex == cursorWs {
			cursorLine = i
			break
		}
	}
	start = cursorLine - maxRows/2
	if start < 0 {
		start = 0
	}
	end = start + maxRows
	if end > len(lines) {
		end = len(lines)
		start = end - maxRows
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

// renderSessionsPane and renderProcsPane render the selected worktree's
// Claude sessions / non-Claude processes -- selecting a worktree on the
// left filters both to just that worktree. Each returns a bordered,
// exactly outerWidth x outerHeight box, same contract as renderWorktreePane.

func (m uiModel) renderSessionsPane(vis []model.Workspace, outerWidth, outerHeight int) string {
	inner := outerWidth - 2
	if inner < 0 {
		inner = 0
	}
	content := detailContent(vis, m.cursor, inner, func(w model.Workspace) []string {
		if len(w.Sessions) == 0 {
			return []string{styleFor(roleDim, false).Render(padField("  (none)", inner))}
		}
		lines := make([]string, len(w.Sessions))
		for i, s := range w.Sessions {
			lines[i] = "  " + RenderSession(s, inner-2)
		}
		return lines
	})
	return renderPane("SESSIONS", content, outerWidth, outerHeight)
}

func (m uiModel) renderProcsPane(vis []model.Workspace, outerWidth, outerHeight int) string {
	inner := outerWidth - 2
	if inner < 0 {
		inner = 0
	}
	content := detailContent(vis, m.cursor, inner, func(w model.Workspace) []string {
		if len(w.Procs) == 0 {
			return []string{styleFor(roleDim, false).Render(padField("  (none)", inner))}
		}
		lines := make([]string, len(w.Procs))
		for i, p := range w.Procs {
			lines[i] = "  " + RenderProc(p, inner-2)
		}
		return lines
	})
	return renderPane("PROCESSES", content, outerWidth, outerHeight)
}

// detailContent builds a detail pane's content lines: a dim subtitle
// carrying the selected workspace's branch (so the pane is legible on its
// own without hunting back to the cursor position), then whatever body
// lines the caller supplies for that workspace.
func detailContent(vis []model.Workspace, cursor, inner int, body func(model.Workspace) []string) []string {
	if len(vis) == 0 || cursor < 0 || cursor >= len(vis) {
		return nil
	}
	w := vis[cursor]
	subtitle := styleFor(roleDim, false).Render(padField(w.Branch, inner))
	return append([]string{subtitle}, body(w)...)
}

// sortWorkspaces orders workspaces by (Repo asc, kindRank asc, Path asc),
// so the sort is fully deterministic across refreshes and the user's own
// checkouts (primary, sibling) always sort above anything an agent created
// (claude-managed, foreign). It does not mutate ws.
func sortWorkspaces(ws []model.Workspace) []model.Workspace {
	sorted := make([]model.Workspace, len(ws))
	copy(sorted, ws)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Repo != sorted[j].Repo {
			return sorted[i].Repo < sorted[j].Repo
		}
		ri, rj := kindRank(sorted[i].Kind), kindRank(sorted[j].Kind)
		if ri != rj {
			return ri < rj
		}
		return sorted[i].Path < sorted[j].Path
	})
	return sorted
}

// Run displays the dashboard and returns the user's choice: the path to cd
// into, what action to take there, and (for a resume) which session id.
//
// ws is painted immediately -- the whole point being that this must never
// block on a live collect. When needsRefresh is true (ws came from a
// persisted snapshot rather than a just-finished Collect), Run kicks off
// refresh in the background right away via Init and swaps the real data in
// as soon as it lands, keeping the cursor on the same workspace. The user
// can also trigger the same refresh on demand with the 'R' key regardless
// of needsRefresh. refresh may be nil (e.g. from a test that has no live
// collector to hand); needsRefresh is then ignored and 'R' is a no-op.
//
// ops carries the trash view's initial data and the callbacks that perform
// deletion, restore and purge; a zero TrashOps (nil callbacks) leaves "d"
// arming reachable but harmless -- see executeDelete -- exactly like refresh
// being nil leaves 'R' a no-op.
//
// unresolvedProcs is ws's matching count of processes whose cwd could not
// be resolved (see discover.Processes.UnresolvedCwds); it is painted
// immediately alongside ws for the same reason ws itself is -- 0 when ws
// came from a persisted snapshot, replaced by the real count as soon as the
// background refresh lands.
func Run(ws []model.Workspace, needsRefresh bool, unresolvedProcs int, refresh func() ([]model.Workspace, int), ops TrashOps) (path string, action Action, sessionID string, err error) {
	mode := ops.Mode
	if mode != "hard" {
		mode = "soft"
	}
	m := uiModel{
		ws:              sortWorkspaces(ws),
		unresolvedProcs: unresolvedProcs,
		refresh:         refresh,
		refreshing:      needsRefresh && refresh != nil,
		trash:           ops.Entries,
		trashMode:       mode,
		del:             ops.Delete,
		restore:         ops.Restore,
		purge:           ops.Purge,
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", ActionNone, "", err
	}
	fm := final.(uiModel)
	// Belt-and-braces: every exit path (Esc, Enter, Tab, q, Ctrl-C) already
	// cancels an in-flight grep search itself, but this is the one place
	// guaranteed to run regardless of which path was taken, so a search
	// left running can never leak its rg process past the program's exit.
	if fm.grep.cancel != nil {
		fm.grep.cancel()
	}
	return fm.chosen, fm.action, fm.session, nil
}

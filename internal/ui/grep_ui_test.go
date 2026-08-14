package ui

import (
	"os/exec"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theczechr/wt/internal/model"
)

// TestHandleGrepEventDropsStaleGeneration is the generation-tagging
// contract from the spec: an event tagged with a generation older than the
// picker's current one must be dropped -- neither appended to hits nor
// requeued for another read -- because it belongs to a search a newer
// keystroke has already superseded.
func TestHandleGrepEventDropsStaleGeneration(t *testing.T) {
	m := uiModel{picker: pickerGrep}
	m.grep.gen = 2
	hit := grepHit{SessionID: "s1"}
	next, cmd := m.Update(grepEvent{gen: 1, hit: &hit})
	if cmd != nil {
		t.Error("a stale-generation event must not requeue a read")
	}
	if got := next.(uiModel).grep.hits; len(got) != 0 {
		t.Errorf("a stale-generation hit must not be appended, got %d hits", len(got))
	}
}

// TestHandleGrepEventAppendsCurrentGenerationAndRequeues asserts the
// opposite: an event tagged with the current generation is appended, and
// another read is requeued so streaming continues.
func TestHandleGrepEventAppendsCurrentGenerationAndRequeues(t *testing.T) {
	ch := make(chan grepEvent, 1)
	m := uiModel{picker: pickerGrep}
	m.grep.gen = 1
	m.grep.ch = ch
	hit := grepHit{SessionID: "s1"}
	next, cmd := m.Update(grepEvent{gen: 1, hit: &hit})
	if cmd == nil {
		t.Fatal("a current-generation hit must requeue another read")
	}
	got := next.(uiModel).grep.hits
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Errorf("hits = %+v, want exactly the one appended hit", got)
	}
}

// TestHandleGrepEventCapsResults asserts the result list stops growing at
// grepMaxResults and sets capped, rather than growing unboundedly for a
// very common query.
func TestHandleGrepEventCapsResults(t *testing.T) {
	ch := make(chan grepEvent, 1)
	m := uiModel{picker: pickerGrep}
	m.grep.gen = 1
	m.grep.ch = ch
	m.grep.hits = make([]grepHit, grepMaxResults)

	next, cmd := m.Update(grepEvent{gen: 1, hit: &grepHit{SessionID: "overflow"}})
	if cmd == nil {
		t.Fatal("a capped hit event must still requeue a read, so the search can keep draining to its done event")
	}
	nm := next.(uiModel)
	if len(nm.grep.hits) != grepMaxResults {
		t.Errorf("hits must stay capped at %d, got %d", grepMaxResults, len(nm.grep.hits))
	}
	if !nm.grep.capped {
		t.Error("capped must be set once the cap is hit")
	}
}

// TestHandleGrepEventDoneClearsSearchingAndRecordsScanned asserts the
// terminal event updates footer-relevant state and does NOT requeue
// another read (the channel is closed after this).
func TestHandleGrepEventDoneClearsSearchingAndRecordsScanned(t *testing.T) {
	m := uiModel{picker: pickerGrep}
	m.grep.gen = 1
	m.grep.searching = true
	next, cmd := m.Update(grepEvent{gen: 1, done: &grepDone{scanned: 274}})
	if cmd != nil {
		t.Error("a done event must not requeue another read")
	}
	nm := next.(uiModel)
	if nm.grep.searching {
		t.Error("done must clear searching")
	}
	if nm.grep.scanned != 274 {
		t.Errorf("scanned = %d, want 274", nm.grep.scanned)
	}
}

// TestGrepDebounceStaleSeqDropped asserts a debounce timer whose seq
// doesn't match the picker's current seq (a later keystroke already armed
// its own timer) is dropped without starting a search or touching gen --
// this is what makes rapid typing not spawn a search per keystroke.
func TestGrepDebounceStaleSeqDropped(t *testing.T) {
	m := uiModel{picker: pickerGrep}
	m.grep.seq = 5
	m.grep.query = "refund"
	next, cmd := m.Update(grepDebounceMsg{seq: 3})
	if cmd != nil {
		t.Error("a stale debounce must not start a search")
	}
	if got := next.(uiModel).grep.gen; got != 0 {
		t.Errorf("a stale debounce must not bump the generation, got gen=%d", got)
	}
}

// TestRestartGrepDebounceClearsStaleResultsAndArmsTimer covers the
// keystroke-time half of debouncing: query-changing keys clear the
// previous query's displayed results immediately (no flash of stale
// matches) and arm a new timer tied to the bumped seq.
func TestRestartGrepDebounceClearsStaleResultsAndArmsTimer(t *testing.T) {
	m := uiModel{picker: pickerGrep}
	m.grep.seq = 1
	m.grep.hits = []grepHit{{SessionID: "old"}}
	m.grep.query = "pay"

	next, cmd := m.restartGrepDebounce()
	if cmd == nil {
		t.Fatal("expected a debounce timer command")
	}
	nm := next.(uiModel)
	if len(nm.grep.hits) != 0 {
		t.Error("changing the query must clear the previous query's results immediately")
	}
	if nm.grep.seq != 2 {
		t.Errorf("seq = %d, want 2 (bumped)", nm.grep.seq)
	}
	msg, ok := cmd().(grepDebounceMsg)
	if !ok {
		t.Fatalf("expected a grepDebounceMsg, got %T", cmd())
	}
	if msg.seq != 2 {
		t.Errorf("timer seq = %d, want 2 (tied to the bump)", msg.seq)
	}
}

// TestRestartGrepDebounceOnEmptyQueryDoesNotArmATimer asserts clearing the
// query back to "" stops searching outright rather than debouncing an
// empty-string search.
func TestRestartGrepDebounceOnEmptyQueryDoesNotArmATimer(t *testing.T) {
	m := uiModel{picker: pickerGrep}
	m.grep.query = ""
	m.grep.searching = true
	next, cmd := m.restartGrepDebounce()
	if cmd != nil {
		t.Error("an empty query must not arm a debounce timer")
	}
	if next.(uiModel).grep.searching {
		t.Error("an empty query must clear searching")
	}
}

// TestStartGrepSearchCancelsPreviousSearch is the cancellation contract
// from the spec: starting a new search must cancel whatever was running
// before it, so an old rg process is never left running once its query has
// been superseded.
func TestStartGrepSearchCancelsPreviousSearch(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home) // keep this test off the real transcript corpus

	called := false
	m := uiModel{picker: pickerGrep}
	m.grep.cancel = func() { called = true }
	m.grep.query = "x"

	next, cmd := m.startGrepSearch()
	if !called {
		t.Fatal("startGrepSearch must cancel the previous in-flight search's context before starting a new one")
	}
	if cmd == nil {
		t.Fatal("expected a listenGrepEvent command for the new search")
	}
	nm := next.(uiModel)
	if nm.grep.gen != 1 {
		t.Errorf("gen = %d, want 1 (bumped from 0)", nm.grep.gen)
	}
	if nm.grep.cancel == nil {
		t.Error("expected a fresh cancel func for the new search")
	}
	nm.stopGrepSearch() // clean up the real rg process this test just spawned
}

// TestStartGrepSearchNoopWhenRgMissing asserts the graceful-degradation
// contract: with grep.err already set (rg missing), starting a search must
// not spawn anything and must leave searching false.
func TestStartGrepSearchNoopWhenRgMissing(t *testing.T) {
	m := uiModel{picker: pickerGrep}
	m.grep.query = "refund"
	m.grep.err = ErrRipgrepNotFound
	next, cmd := m.startGrepSearch()
	if cmd != nil {
		t.Error("must not start a search when rg is known missing")
	}
	if next.(uiModel).grep.searching {
		t.Error("searching must stay false")
	}
}

// TestNewGrepStateSurfacesMissingRipgrep stubs lookRipgrep to simulate an
// environment without rg on PATH, and asserts the picker degrades to a
// clear error rather than crashing or silently doing nothing.
func TestNewGrepStateSurfacesMissingRipgrep(t *testing.T) {
	prev := lookRipgrep
	lookRipgrep = func() (string, error) { return "", exec.ErrNotFound }
	defer func() { lookRipgrep = prev }()

	g := newGrepState(nil)
	if g.err != ErrRipgrepNotFound {
		t.Errorf("err = %v, want ErrRipgrepNotFound", g.err)
	}
}

// TestUpdateGrepEnterResumesKnownSessionOnly asserts Enter on a known hit
// sets ActionResume with its session id, and is a no-op on an Untracked hit
// (there is nowhere to resume/cd to).
func TestUpdateGrepEnterResumesKnownSessionOnly(t *testing.T) {
	ws := model.Workspace{Path: "/u/server-cache"}
	m := uiModel{picker: pickerGrep}
	m.grep.hits = []grepHit{{Workspace: ws, SessionID: "sess1"}}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected tea.QuitMsg")
	}
	nm := next.(uiModel)
	if nm.chosen != "/u/server-cache" || nm.session != "sess1" || nm.action != ActionResume {
		t.Errorf("got chosen=%q session=%q action=%v, want /u/server-cache sess1 ActionResume", nm.chosen, nm.session, nm.action)
	}

	m2 := uiModel{picker: pickerGrep}
	m2.grep.hits = []grepHit{{Untracked: true, SessionID: "sess2"}}
	next2, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 != nil {
		t.Error("Enter on an untracked hit must be a no-op -- there is nowhere to resume to")
	}
	if next2.(uiModel).action != ActionNone {
		t.Error("an untracked hit must never set an action")
	}
}

// TestUpdateGrepTabCdsWithoutResuming covers Tab's distinct contract from
// Enter: cd only, no session id, still gated on Untracked.
func TestUpdateGrepTabCdsWithoutResuming(t *testing.T) {
	ws := model.Workspace{Path: "/u/server-cache"}
	m := uiModel{picker: pickerGrep}
	m.grep.hits = []grepHit{{Workspace: ws, SessionID: "sess1"}}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if cmd == nil {
		t.Fatal("expected tea.Quit")
	}
	nm := next.(uiModel)
	if nm.chosen != "/u/server-cache" || nm.session != "" || nm.action != ActionCd {
		t.Errorf("got chosen=%q session=%q action=%v, want /u/server-cache \"\" ActionCd", nm.chosen, nm.session, nm.action)
	}
}

// TestUpdateGrepNavigationUsesArrowsAndCtrlNP mirrors the find picker's own
// navigation test: j/k must be literal query text here too.
func TestUpdateGrepNavigationUsesArrowsAndCtrlNP(t *testing.T) {
	m := uiModel{picker: pickerGrep}
	m.grep.hits = []grepHit{{SessionID: "a"}, {SessionID: "b"}, {SessionID: "c"}}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	nm := next.(uiModel)
	if nm.grep.query != "k" {
		t.Errorf(`"k" must be literal query text, got query=%q`, nm.grep.query)
	}
	if nm.grep.cursor != 0 {
		t.Error("typing must not move the cursor")
	}

	m2 := uiModel{picker: pickerGrep}
	m2.grep.hits = []grepHit{{SessionID: "a"}, {SessionID: "b"}, {SessionID: "c"}}
	next2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if got := next2.(uiModel).grep.cursor; got != 1 {
		t.Fatalf("ctrl+n: cursor = %d, want 1", got)
	}
}

// TestUpdateGrepEscCancelsInFlightSearch asserts Esc both leaves the
// picker AND cancels any in-flight search -- an abandoned search must not
// keep running once the user has backed out of the picker entirely.
func TestUpdateGrepEscCancelsInFlightSearch(t *testing.T) {
	called := false
	m := uiModel{picker: pickerGrep}
	m.grep.cancel = func() { called = true }

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("Esc must not return a command")
	}
	if !called {
		t.Error("Esc must cancel any in-flight search")
	}
	nm := next.(uiModel)
	if nm.picker != pickerNone {
		t.Error("Esc must return to the dashboard")
	}
	if nm.grep.cancel != nil {
		t.Error("Esc must clear the cancel func once invoked")
	}
}

// TestGrepFooterCounts covers the summary line's three states: actively
// searching, a completed sweep, and a capped result set.
func TestGrepFooterCounts(t *testing.T) {
	hits := []grepHit{{SessionID: "a"}, {SessionID: "a"}, {SessionID: "b"}} // 2 distinct sessions

	if got := grepFooterCounts(hits, 0, false, true); got != "2 sessions · searching…" {
		t.Errorf("searching: got %q", got)
	}
	if got := grepFooterCounts(hits, 274, false, false); got != "2 sessions · searched 274 transcripts" {
		t.Errorf("done: got %q", got)
	}
	if got := grepFooterCounts(hits, 200, true, false); got != "2 sessions · searched 200 transcripts (capped)" {
		t.Errorf("capped: got %q", got)
	}
	if got := grepFooterCounts(nil, 1, false, false); got != "0 sessions · searched 1 transcript" {
		t.Errorf("singular transcript: got %q", got)
	}
}

// TestRelTime spot-checks the relative-timestamp buckets used in the grep
// hit header.
func TestRelTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "—"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-2 * 24 * time.Hour), "2d ago"},
	}
	for _, c := range cases {
		if got := relTime(c.t); got != c.want {
			t.Errorf("relTime(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

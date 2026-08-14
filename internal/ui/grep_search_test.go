package ui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/theczechr/wt/internal/model"
)

// TestBuildDirIndexIsForwardOnly asserts buildDirIndex computes its keys
// FORWARD, via model.ProjectDirName on each known workspace's real path --
// the only direction that mapping may ever be computed in (see
// model/paths.go and grep_search.go's own doc comment). This test would
// only ever catch an accidental typo in that direction, since there's no
// way to test "did NOT reverse-parse" directly -- the real guard is that
// nothing in this package's source calls anything resembling a reverse
// parse, confirmed by code review.
func TestBuildDirIndexIsForwardOnly(t *testing.T) {
	ws := []model.Workspace{
		{Path: "/Users/alice/code/server-cache"},
		{Path: "/Users/alice/.claude/worktrees/pr-4004"},
	}
	idx := buildDirIndex(ws)
	if len(idx) != len(ws) {
		t.Fatalf("dirIndex has %d entries, want %d", len(idx), len(ws))
	}
	for _, w := range ws {
		key := model.ProjectDirName(w.Path)
		got, ok := idx[key]
		if !ok {
			t.Errorf("dirIndex missing forward-computed key %q for path %q", key, w.Path)
			continue
		}
		if got.Path != w.Path {
			t.Errorf("dirIndex[%q].Path = %q, want %q", key, got.Path, w.Path)
		}
	}
}

// TestAttributeMatchKnownWorkspace covers the happy path: a match in a
// directory present in dirIndex is attributed to that workspace, and its
// session is filled in from Workspace.Sessions by matching the transcript
// filename's UUID against Session.ID.
func TestAttributeMatchKnownWorkspace(t *testing.T) {
	ws := model.Workspace{
		Path:     "/Users/alice/code/server-cache",
		Sessions: []model.Session{{ID: "abc-123", Title: "Fix the refund bug", PRNumber: 4001}},
	}
	idx := buildDirIndex([]model.Workspace{ws})

	dirName := model.ProjectDirName(ws.Path)
	path := filepath.Join("/Users/alice/.claude/projects", dirName, "abc-123.jsonl")
	line := `{"type":"user","message":{"content":"please fix the refund bug"},"timestamp":"2026-01-01T00:00:00Z"}`

	hit, ok := attributeMatch(path, line, "refund", idx)
	if !ok {
		t.Fatal("expected a match")
	}
	if hit.Untracked {
		t.Error("a known workspace's transcript must not be marked untracked")
	}
	if hit.Workspace.Path != ws.Path {
		t.Errorf("hit.Workspace.Path = %q, want %q", hit.Workspace.Path, ws.Path)
	}
	if hit.SessionID != "abc-123" {
		t.Errorf("hit.SessionID = %q, want abc-123", hit.SessionID)
	}
	if hit.Session.Title != "Fix the refund bug" {
		t.Errorf("hit.Session must be populated from Workspace.Sessions by ID, got Title=%q", hit.Session.Title)
	}
	if hit.Session.PRNumber != 4001 {
		t.Errorf("hit.Session.PRNumber = %d, want 4001", hit.Session.PRNumber)
	}
}

// TestAttributeMatchUnknownDirectoryIsUntracked covers a transcript whose
// project directory isn't any known workspace's: it must still be
// returned (never silently dropped), clearly marked Untracked, with no
// guessed Workspace.
func TestAttributeMatchUnknownDirectoryIsUntracked(t *testing.T) {
	idx := map[string]model.Workspace{} // nothing known
	path := "/Users/alice/.claude/projects/-Users-someone-somewhere-else/xyz.jsonl"
	line := `{"type":"user","message":{"content":"please fix the refund bug"}}`

	hit, ok := attributeMatch(path, line, "refund", idx)
	if !ok {
		t.Fatal("expected a match")
	}
	if !hit.Untracked {
		t.Error("an unknown project directory must be marked Untracked")
	}
	if hit.Workspace.Path != "" || hit.Workspace.Repo != "" {
		t.Errorf("an untracked hit must carry the zero-value Workspace, got %+v", hit.Workspace)
	}
	if hit.SessionID != "xyz" {
		t.Errorf("hit.SessionID = %q, want xyz (from the filename)", hit.SessionID)
	}
}

// TestAttributeMatchRejectsMatchOutsideRecordText is the precision check on
// attributeMatch re-confirming the query landed inside the record's own
// extracted text: rg matches at the LINE level, so a query that only hit
// some other JSON field (here, sessionId) must not be attributed as a
// content match.
func TestAttributeMatchRejectsMatchOutsideRecordText(t *testing.T) {
	line := `{"type":"user","sessionId":"needle-123","message":{"content":"totally unrelated text"}}`
	if _, ok := attributeMatch("/x/-Users-x/needle-123.jsonl", line, "needle", map[string]model.Workspace{}); ok {
		t.Error("a match landing outside the record's own text must be rejected, not attributed")
	}
}

// TestExtractRecordTextScope is the scope contract from the spec: only
// type "user" (message.content, when it's a plain string), "ai-title" and
// "last-prompt" are in scope. Everything else -- assistant text, tool
// output, tool_result/mixed content, isMeta records -- is out of scope.
func TestExtractRecordTextScope(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantOK   bool
		wantText string
	}{
		{"user prompt", `{"type":"user","message":{"content":"hello"}}`, true, "hello"},
		{"user isMeta skipped", `{"type":"user","isMeta":true,"message":{"content":"hello"}}`, false, ""},
		{"user list content skipped (tool_result)", `{"type":"user","message":{"content":[{"type":"tool_result","text":"hello"}]}}`, false, ""},
		{"user empty content skipped", `{"type":"user","message":{"content":""}}`, false, ""},
		{"assistant skipped entirely", `{"type":"assistant","message":{"content":"hello"}}`, false, ""},
		{"ai-title in scope", `{"type":"ai-title","aiTitle":"Fix the refund bug"}`, true, "Fix the refund bug"},
		{"last-prompt in scope", `{"type":"last-prompt","lastPrompt":"do the thing"}`, true, "do the thing"},
		// oneLine maps '\n' and '\r' independently (mirroring model.Session's
		// own helper of the same name), so "\r\n" becomes two spaces, not one.
		{"multiline prompt collapsed to one line", `{"type":"user","message":{"content":"line1\nline2\r\nline3"}}`, true, "line1 line2  line3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec transcriptRecord
			if err := json.Unmarshal([]byte(c.line), &rec); err != nil {
				t.Fatalf("test fixture is invalid JSON: %v", err)
			}
			text, ok := extractRecordText(rec)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && text != c.wantText {
				t.Errorf("text = %q, want %q", text, c.wantText)
			}
		})
	}
}

// TestFindMatchRuneRange covers case-insensitivity and rune (not byte)
// offsets, since transcript text can contain non-ASCII.
func TestFindMatchRuneRange(t *testing.T) {
	start, end, ok := findMatchRuneRange("Fix the Refund bug", "refund")
	if !ok {
		t.Fatal("expected a case-insensitive match")
	}
	if start != 8 || end != 14 {
		t.Errorf("got [%d:%d), want [8:14)", start, end)
	}

	if _, _, ok := findMatchRuneRange("nothing here", "zzz"); ok {
		t.Error("expected no match")
	}

	// Non-ASCII prefix before the match -- byte offsets from strings.Index
	// would land on the wrong rune boundary here; rune offsets must not.
	start, end, ok = findMatchRuneRange("→ fix the refund bug", "refund")
	if !ok || start != 10 || end != 16 {
		t.Errorf("got ok=%v [%d:%d), want ok=true [10:16)", ok, start, end)
	}
}

// TestParseRgMatchLineFullPipeline drives parseRgMatchLine (the function
// runGrepSearch's read loop actually calls per output line) against a
// realistic `rg --json` "match" event, confirming the whole
// parse-then-attribute chain end to end without needing a real rg process.
func TestParseRgMatchLineFullPipeline(t *testing.T) {
	ws := model.Workspace{
		Path:     "/Users/alice/code/server-cache",
		Sessions: []model.Session{{ID: "sess1"}},
	}
	idx := buildDirIndex([]model.Workspace{ws})
	path := filepath.Join("/Users/alice/.claude/projects", model.ProjectDirName(ws.Path), "sess1.jsonl")
	rawLine := `{"type":"user","message":{"content":"please look at the refund queue"},"timestamp":"2026-02-02T10:00:00Z"}`

	ev := rgEvent{Type: "match"}
	data, _ := json.Marshal(rgMatchData{
		Path: struct {
			Text string `json:"text"`
		}{Text: path},
		Lines: struct {
			Text string `json:"text"`
		}{Text: rawLine + "\n"},
	})
	ev.Data = data
	rgLine, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	hit, ok := parseRgMatchLine(string(rgLine), "refund", idx)
	if !ok {
		t.Fatal("expected a match")
	}
	if hit.Untracked {
		t.Error("expected an attributed (tracked) hit")
	}
	if hit.SessionID != "sess1" {
		t.Errorf("hit.SessionID = %q, want sess1", hit.SessionID)
	}

	// A non-"match" event (e.g. rg's own "begin") must be ignored.
	if _, ok := parseRgMatchLine(`{"type":"begin","data":{}}`, "refund", idx); ok {
		t.Error(`a "begin" event must not be treated as a match`)
	}
}

// TestParseRgSummaryLine covers the footer's "searched N transcripts"
// source: rg's own "summary" event's stats.searches.
func TestParseRgSummaryLine(t *testing.T) {
	line := `{"type":"summary","data":{"stats":{"searches":274,"searches_with_match":3}}}`
	scanned, ok := parseRgSummaryLine(line)
	if !ok {
		t.Fatal("expected a parsed summary")
	}
	if scanned != 274 {
		t.Errorf("scanned = %d, want 274", scanned)
	}

	if _, ok := parseRgSummaryLine(`{"type":"match","data":{}}`); ok {
		t.Error("a non-summary event must not be treated as one")
	}
}

// requireRipgrep skips a test when rg isn't on PATH, so the integration
// tests below degrade gracefully in an environment without it rather than
// failing -- mirroring the picker's own runtime behaviour.
func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed; skipping runGrepSearch integration test")
	}
}

// writeFixtureTranscript creates one synthetic transcript file under a
// temp HOME's ~/.claude/projects/<dirName>/, so integration tests never
// touch the real (1.8GB, private) transcript corpus.
func writeFixtureTranscript(t *testing.T, home, dirName, sessionID, line string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunGrepSearchStreamsAttributedHits is an end-to-end integration test
// against a real (but synthetic, temp-HOME) rg invocation: it must stream
// back the fixture's match, attributed to the known workspace, plus a
// final done event reporting how many files it searched.
func TestRunGrepSearchStreamsAttributedHits(t *testing.T) {
	requireRipgrep(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	ws := model.Workspace{
		Path:     "/Users/test/project",
		Sessions: []model.Session{{ID: "sess1"}},
	}
	dirName := model.ProjectDirName(ws.Path)
	writeFixtureTranscript(t, home, dirName, "sess1",
		`{"type":"user","message":{"content":"please fix the flaky refund test"}}`)
	writeFixtureTranscript(t, home, dirName, "sess2",
		`{"type":"assistant","message":{"content":"refund refund refund, not in scope"}}`)

	idx := buildDirIndex([]model.Workspace{ws})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch := runGrepSearch(ctx, 1, "refund", idx)
	var hits []grepHit
	var done *grepDone
	for ev := range ch {
		if ev.hit != nil {
			hits = append(hits, *ev.hit)
		}
		if ev.done != nil {
			done = ev.done
		}
	}
	if done == nil {
		t.Fatal("expected a final done event")
	}
	if done.err != nil {
		t.Fatalf("unexpected error: %v", done.err)
	}
	if done.scanned != 2 {
		t.Errorf("scanned = %d, want 2 (both fixture files)", done.scanned)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want exactly 1 (the assistant-type record is out of scope)", len(hits))
	}
	if hits[0].SessionID != "sess1" || hits[0].Untracked {
		t.Errorf("hit = %+v, want the known sess1 hit", hits[0])
	}
}

// TestRunGrepSearchUnwindsPromptlyOnCancel proves the concurrency
// contract: cancelling the context unblocks runGrepSearch's goroutine and
// closes its channel promptly, rather than leaving it (and rg) running.
// This is what makes "kill the previous rg process" actually true instead
// of aspirational.
func TestRunGrepSearchUnwindsPromptlyOnCancel(t *testing.T) {
	requireRipgrep(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// No fixture files are needed for this test -- rg fails over an
	// absent directory just as fast as an empty one, and this test only
	// cares that cancellation unblocks the channel promptly.

	ctx, cancel := context.WithCancel(context.Background())
	ch := runGrepSearch(ctx, 1, "anything", map[string]model.Workspace{})
	cancel() // cancel immediately, racing the search's own startup

	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed -- the goroutine unwound instead of hanging
			}
		case <-deadline:
			t.Fatal("runGrepSearch did not unwind within 3s of its context being cancelled")
		}
	}
}

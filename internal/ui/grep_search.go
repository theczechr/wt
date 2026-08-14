// Grep sessions: a streaming search over Claude Code transcripts, driven by
// `rg --json`. This file is the search engine -- process management, JSONL
// parsing, and attributing a hit back to a known workspace; grep_ui.go wires
// it into bubbletea (debounce, generation-tagged streaming, rendering).
//
// Measured on the machine this was built against: a full sweep of 677
// transcripts (1.8GB) takes ~720ms, but time to first match is ~28ms -- so
// results stream in as they're found rather than waiting for rg to exit.
package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theczechr/wt/internal/model"
)

// ErrRipgrepNotFound is surfaced in the grep picker when rg isn't on PATH:
// the picker shows the message and disables search, but the rest of the TUI
// keeps working.
var ErrRipgrepNotFound = errors.New("ripgrep (rg) not found — grep needs it")

// lookRipgrep is a var, not a direct exec.LookPath call, so tests can stub
// "rg is missing" without needing to manipulate PATH.
var lookRipgrep = func() (string, error) { return exec.LookPath("rg") }

// grepMaxResults caps the streamed result list, so a very common query
// cannot flood the picker. The footer notes when the cap was hit.
const grepMaxResults = 200

// grepHit is one query match, attributed back to a workspace when possible.
type grepHit struct {
	Workspace  model.Workspace // zero value when Untracked
	Untracked  bool            // the transcript's project dir isn't any known workspace's -- there is nowhere to cd to
	SessionID  string
	Session    model.Session // populated (Label(), PRNumber, Live, ...) when the session is one of Workspace.Sessions; zero value when Untracked
	Text       string        // the matched record's plain text (a prompt or an ai-title)
	MatchStart int           // rune offset of the query match within Text
	MatchEnd   int
	Timestamp  time.Time // zero when the record carried no parseable timestamp
}

// grepEvent is one item streamed back from a running search: either a hit
// or, as the final event before its channel closes, a grepDone. gen ties it
// to the generation of search that produced it.
type grepEvent struct {
	gen  int
	hit  *grepHit
	done *grepDone
}

// grepDone reports how a search finished: how many transcript files ripgrep
// searched (for the "searched N transcripts" footer) and any hard failure
// starting rg. A search that found zero matches, or that was cancelled
// mid-flight, is not an error -- only a genuine failure to run rg is.
type grepDone struct {
	scanned int
	err     error
}

// buildDirIndex maps a Claude Code project directory name (e.g.
// "-Users-alice-code-backend-cache") to the workspace it belongs
// to, computed FORWARD from each known workspace's real path via
// model.ProjectDirName. This is the only direction that mapping may be
// computed in -- model.ProjectDirName's own doc comment is explicit that
// flattening a path is lossy and one-way, and nothing in this package (or
// anywhere else) may parse a directory name back into a path. A transcript
// whose directory isn't a key in this map belongs to a worktree wt doesn't
// track; grepHit.Untracked carries that instead of guessing a path for it.
func buildDirIndex(ws []model.Workspace) map[string]model.Workspace {
	idx := make(map[string]model.Workspace, len(ws))
	for _, w := range ws {
		idx[model.ProjectDirName(w.Path)] = w
	}
	return idx
}

// runGrepSearch launches `rg --json` over ~/.claude/projects and streams
// attributed hits back on the returned channel, tagged with gen, followed by
// exactly one grepDone before the channel closes. Cancelling ctx kills the
// rg process (exec.CommandContext) and unblocks any pending channel send
// (the internal select on ctx.Done()), so an abandoned search can never
// leak a goroutine or leave rg running.
func runGrepSearch(ctx context.Context, gen int, query string, dirIndex map[string]model.Workspace) <-chan grepEvent {
	ch := make(chan grepEvent)
	go func() {
		defer close(ch)
		send := func(ev grepEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		projectsDir := model.ClaudeProjectsDir()
		// -F: fixed-string (the query is never a regex). -S: smart-case.
		// --glob restricts the walk to transcript files. "--" ends option
		// parsing so a query that happens to start with "-" is never
		// misread as a flag.
		cmd := exec.CommandContext(ctx, "rg",
			"--json", "-F", "-S", "--glob", "*.jsonl", "--", query, projectsDir)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			send(grepEvent{gen: gen, done: &grepDone{err: err}})
			return
		}
		if err := cmd.Start(); err != nil {
			send(grepEvent{gen: gen, done: &grepDone{err: err}})
			return
		}

		scanned := 0
		reader := bufio.NewReaderSize(stdout, 64*1024)
		for {
			line, readErr := reader.ReadString('\n')
			if len(line) > 0 {
				if hit, ok := parseRgMatchLine(line, query, dirIndex); ok {
					if !send(grepEvent{gen: gen, hit: &hit}) {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						return
					}
				} else if n, ok := parseRgSummaryLine(line); ok {
					scanned = n
				}
			}
			if readErr != nil {
				break
			}
		}
		_ = cmd.Wait() // reap; a non-nil error here is expected on cancellation (the process was killed) and carries nothing worth surfacing
		send(grepEvent{gen: gen, done: &grepDone{scanned: scanned}})
	}()
	return ch
}

// rgEvent is one line of `rg --json` output; Type selects how Data is
// shaped ("match" vs "summary" -- rg also emits "begin"/"end" per file,
// which carry nothing this package needs).
type rgEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type rgMatchData struct {
	Path struct {
		Text string `json:"text"`
	} `json:"path"`
	Lines struct {
		Text string `json:"text"`
	} `json:"lines"`
}

type rgSummaryData struct {
	Stats struct {
		Searches int `json:"searches"` // total files rg searched, matched or not -- the "searched N transcripts" footer count
	} `json:"stats"`
}

func parseRgSummaryLine(line string) (scanned int, ok bool) {
	var ev rgEvent
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "summary" {
		return 0, false
	}
	var d rgSummaryData
	if json.Unmarshal(ev.Data, &d) != nil {
		return 0, false
	}
	return d.Stats.Searches, true
}

// parseRgMatchLine parses one `rg --json` "match" event and attributes it
// to a workspace. It returns ok=false for any non-match event, any
// transcript record outside the user-prompt/title scope (assistant text,
// tool output, tool results, isMeta records), or a match that only landed
// in some other JSON field than the record's own text (e.g. inside "cwd" or
// "sessionId") rather than the prompt/title itself.
func parseRgMatchLine(line, query string, dirIndex map[string]model.Workspace) (grepHit, bool) {
	var ev rgEvent
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "match" {
		return grepHit{}, false
	}
	var d rgMatchData
	if json.Unmarshal(ev.Data, &d) != nil {
		return grepHit{}, false
	}
	return attributeMatch(d.Path.Text, d.Lines.Text, query, dirIndex)
}

// transcriptRecord is the subset of a Claude Code transcript JSONL record
// this package cares about. See discover.record (internal/discover/session.go)
// for the sibling used by the tail reader -- kept separate here because this
// one additionally needs Message.Content and IsMeta, which that one doesn't.
type transcriptRecord struct {
	Type       string             `json:"type"`
	AITitle    string             `json:"aiTitle"`
	LastPrompt string             `json:"lastPrompt"`
	IsMeta     bool               `json:"isMeta"`
	Timestamp  string             `json:"timestamp"`
	Message    *transcriptMessage `json:"message"`
}

type transcriptMessage struct {
	Content json.RawMessage `json:"content"`
}

// attributeMatch turns one transcript line plus the file path rg found it
// in into a grepHit: extracts the in-scope text (user prompt / ai-title /
// last-prompt), confirms the query actually matched inside THAT text rather
// than some other field on the line, and looks up the owning workspace by
// the transcript directory's name -- forward, via dirIndex, never by
// parsing the path.
func attributeMatch(path, rawLine, query string, dirIndex map[string]model.Workspace) (grepHit, bool) {
	var rec transcriptRecord
	if json.Unmarshal([]byte(rawLine), &rec) != nil {
		return grepHit{}, false
	}
	text, ok := extractRecordText(rec)
	if !ok {
		return grepHit{}, false
	}
	start, end, ok := findMatchRuneRange(text, query)
	if !ok {
		return grepHit{}, false
	}

	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	hit := grepHit{
		SessionID:  sessionID,
		Text:       text,
		MatchStart: start,
		MatchEnd:   end,
	}
	if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		hit.Timestamp = ts
	}

	dirName := filepath.Base(filepath.Dir(path))
	ws, known := dirIndex[dirName]
	if !known {
		hit.Untracked = true
		return hit, true
	}
	hit.Workspace = ws
	for _, s := range ws.Sessions {
		if s.ID == sessionID {
			hit.Session = s
			if hit.Timestamp.IsZero() {
				hit.Timestamp = s.Mtime
			}
			break
		}
	}
	return hit, true
}

// extractRecordText returns the in-scope, human-typed text of one
// transcript record: a user prompt (type "user", message.content, skipping
// isMeta records and any record whose content is a list rather than a plain
// string -- almost all list content is tool_result/tool_use, i.e. Claude's
// own output, not something the user typed), an AI-generated session title
// (type "ai-title"), or the cached last-prompt (type "last-prompt"). Every
// other record type -- most importantly "assistant", which is where the
// 1.8GB of transcript bulk actually lives -- is out of scope and returns
// ok=false.
func extractRecordText(rec transcriptRecord) (text string, ok bool) {
	switch rec.Type {
	case "ai-title":
		return oneLine(rec.AITitle), rec.AITitle != ""
	case "last-prompt":
		return oneLine(rec.LastPrompt), rec.LastPrompt != ""
	case "user":
		if rec.IsMeta || rec.Message == nil {
			return "", false
		}
		var content string
		if json.Unmarshal(rec.Message.Content, &content) != nil {
			return "", false // a list (tool_result / mixed content), not a plain typed prompt
		}
		return oneLine(content), content != ""
	default:
		return "", false
	}
}

// oneLine collapses newlines to single spaces, mirroring model.Session's
// own (unexported) helper of the same purpose: a real prompt is routinely
// multi-line (pasted code, multi-paragraph instructions), and every
// consumer of grepHit.Text renders it as a single, exact-width screen line
// -- an embedded "\n" would otherwise split a row across two physical
// terminal lines, breaking every width invariant this package relies on.
func oneLine(s string) string {
	f := func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}
	return strings.Map(f, s)
}

// findMatchRuneRange reports the rune offsets of query's first
// case-insensitive occurrence within text. Rune (not byte) offsets, since
// text can contain non-ASCII and every consumer of this range (elision,
// highlighting) does its own width math in runes via lipgloss.Width, never
// len().
func findMatchRuneRange(text, query string) (start, end int, ok bool) {
	if query == "" {
		return 0, 0, false
	}
	idx := strings.Index(strings.ToLower(text), strings.ToLower(query))
	if idx < 0 {
		return 0, 0, false
	}
	start = utf8.RuneCountInString(text[:idx])
	end = start + utf8.RuneCountInString(query)
	return start, end, true
}

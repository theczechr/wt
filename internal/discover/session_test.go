package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Record shapes below are copied from a real transcript on this machine.
const transcript = `{"type":"user","cwd":"/tmp/wtA","gitBranch":"develop","timestamp":"2026-07-08T08:31:51.581Z","message":{"content":[{"text":"first prompt"}]}}
{"type":"ai-title","aiTitle":"An early title","sessionId":"abc"}
{"type":"assistant","message":{"content":[{"text":"..."}]}}
{"type":"pr-link","sessionId":"abc","prNumber":4005,"prUrl":"https://github.com/acme-org/server/pull/4005","prRepository":"acme-org/server"}
{"type":"ai-title","aiTitle":"Extend checkout with three payment aggregators","sessionId":"abc"}
{"type":"last-prompt","lastPrompt":"we should not show the dollars","leafUuid":"x","sessionId":"abc"}
`

func TestReadSessionTailTakesLastOfEachRecordType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(p, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSessionTail(p)
	if err != nil {
		t.Fatalf("ReadSessionTail: %v", err)
	}
	if s.ID != "abc" {
		t.Errorf("ID = %q, want abc", s.ID)
	}
	if s.Title != "Extend checkout with three payment aggregators" {
		t.Errorf("Title = %q (must be the LAST ai-title)", s.Title)
	}
	if s.LastPrompt != "we should not show the dollars" {
		t.Errorf("LastPrompt = %q", s.LastPrompt)
	}
	if s.PRNumber != 4005 {
		t.Errorf("PRNumber = %d, want 4005", s.PRNumber)
	}
	if s.Branch != "develop" {
		t.Errorf("Branch = %q, want develop", s.Branch)
	}
	if s.Cwd != "/tmp/wtA" {
		t.Errorf("Cwd = %q, want /tmp/wtA", s.Cwd)
	}
}

func TestReadSessionTailStopsBeforeReadingWholeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.jsonl")
	// 40k junk lines ahead of the useful tail; a top-down parser would
	// have to walk all of them.
	var b strings.Builder
	for i := 0; i < 40000; i++ {
		b.WriteString(`{"type":"assistant","message":{"content":[{"text":"noise"}]}}` + "\n")
	}
	b.WriteString(`{"type":"ai-title","aiTitle":"Tail title","sessionId":"big"}` + "\n")
	b.WriteString(`{"type":"last-prompt","lastPrompt":"tail prompt","sessionId":"big"}` + "\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSessionTail(p)
	if err != nil {
		t.Fatalf("ReadSessionTail: %v", err)
	}
	if s.Title != "Tail title" || s.LastPrompt != "tail prompt" {
		t.Errorf("got Title=%q LastPrompt=%q", s.Title, s.LastPrompt)
	}
	if s.BytesScanned > 256*1024 {
		t.Errorf("scanned %d bytes; tail read must stay bounded", s.BytesScanned)
	}
}

// TestReadSessionTailBytesScannedNeverExceedsMaxTail guards the maxTail
// budget clamp: step was only ever clamped against what remained in the
// file, not against what remained of the maxTail budget. That's a no-op
// today because chunkSize (64KB) evenly divides maxTail (256KB), but the
// invariant -- BytesScanned never exceeds maxTail -- is exactly what the
// clamp exists to guarantee regardless of how those constants are tuned.
func TestReadSessionTailBytesScannedNeverExceedsMaxTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.jsonl")
	// No pr-link or user record at all, so `complete` can never be
	// satisfied and the loop only stops once the maxTail budget is spent --
	// exercising the full clamp path rather than stopping early.
	var b strings.Builder
	for b.Len() < 512*1024 {
		b.WriteString(`{"type":"assistant","message":{"content":[{"text":"noise"}]}}` + "\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSessionTail(p)
	if err != nil {
		t.Fatalf("ReadSessionTail: %v", err)
	}
	const maxTail = 256 * 1024
	if s.BytesScanned > maxTail {
		t.Errorf("BytesScanned = %d, must never exceed maxTail (%d)", s.BytesScanned, maxTail)
	}
}

func TestReadSessionTailOnCorruptFileDegrades(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(p, []byte("not json at all\n{{{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSessionTail(p); err != nil {
		t.Errorf("corrupt transcript must not error, got %v", err)
	}
}

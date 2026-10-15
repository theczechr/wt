package discover

import (
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/model"
)

func TestParsePRListKeysByBranch(t *testing.T) {
	body := []byte(`[
	  {"number":4003,"headRefName":"feature/refund-payment-queue","state":"OPEN"},
	  {"number":4002,"headRefName":"feature/invite-code-split","state":"MERGED"}
	]`)
	got := ParsePRList(body)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got["feature/refund-payment-queue"].Number != 4003 {
		t.Errorf("wrong number: %+v", got["feature/refund-payment-queue"])
	}
	if got["feature/invite-code-split"].State != "MERGED" {
		t.Errorf("wrong state: %+v", got["feature/invite-code-split"])
	}
}

func TestParsePRListOnGarbageReturnsEmptyNotPanic(t *testing.T) {
	if got := ParsePRList([]byte("gh: command not found")); len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestPRCacheRoundTripAndExpiry(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())

	dir := t.TempDir()
	in := map[string]model.PR{"develop": {Number: 1, State: "OPEN"}}
	if err := WritePRCache(dir, in); err != nil {
		t.Fatal(err)
	}
	out, ok := CachedPRs(dir, time.Hour)
	if !ok || out["develop"].Number != 1 {
		t.Errorf("fresh cache should hit, got ok=%v out=%v", ok, out)
	}
	if _, ok := CachedPRs(dir, -time.Second); ok {
		t.Error("expired cache must miss")
	}
}

// gh lists newest first. A reused branch name must resolve to its OPEN PR,
// never to an old CLOSED one listed after it.
func TestParsePRListPrefersOpenThenNewest(t *testing.T) {
	got := ParsePRList([]byte(`[
	  {"number":9,"headRefName":"feat/reused","state":"OPEN"},
	  {"number":5,"headRefName":"feat/reused","state":"CLOSED"},
	  {"number":3,"headRefName":"feat/old","state":"MERGED"},
	  {"number":12,"headRefName":"feat/old","state":"CLOSED"},
	  {"number":7,"headRefName":"feat/old","state":"MERGED"}
	]`))
	if pr := got["feat/reused"]; pr.Number != 9 || pr.State != "OPEN" {
		t.Errorf("feat/reused = %+v, want the OPEN #9", pr)
	}
	if pr := got["feat/old"]; pr.Number != 12 || pr.State != "CLOSED" {
		t.Errorf("feat/old = %+v, want the newest #12", pr)
	}
}

package discover

import (
	"testing"
	"time"

	"github.com/theczechr/wt/internal/model"
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

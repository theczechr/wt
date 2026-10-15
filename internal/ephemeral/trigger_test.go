package ephemeral_test

import (
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
)

func TestTriggerDueRateLimits(t *testing.T) {
	t.Setenv("WT_STATE_DIR", t.TempDir())
	now := time.Now()
	if !ephemeral.TriggerDue(now, 10*time.Minute) {
		t.Fatal("first call must be due")
	}
	if ephemeral.TriggerDue(now.Add(time.Minute), 10*time.Minute) {
		t.Fatal("a call within the interval must not be due")
	}
	if !ephemeral.TriggerDue(now.Add(11*time.Minute), 10*time.Minute) {
		t.Fatal("a call after the interval must be due")
	}
}

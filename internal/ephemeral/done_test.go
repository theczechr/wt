package ephemeral_test

import (
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	base := ephemeral.Facts{IdleDays: 3, Now: now, LastUsed: now.Add(-time.Hour), OpenedAt: now.Add(-2 * time.Hour)}

	cases := []struct {
		name     string
		edit     func(*ephemeral.Facts)
		done     bool
		contains string
	}{
		{"review: no own commits", func(f *ephemeral.Facts) {}, true, "no commits of your own"},
		{"own commits, PR open, fresh", func(f *ephemeral.Facts) { f.OwnCommits = 2; f.PRState = "OPEN" }, false, "PR open"},
		{"own commits, no PR, fresh", func(f *ephemeral.Facts) { f.OwnCommits = 1 }, false, "no PR"},
		{"own commits, PR merged", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.PRState = "MERGED" }, true, "PR merged"},
		{"own commits, PR closed", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.PRState = "CLOSED" }, true, "PR closed"},
		{"own commits, idle past limit", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.LastUsed = now.Add(-4 * day) }, true, "idle"},
		{"own commits, idle exactly at limit", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.LastUsed = now.Add(-3 * day) }, true, "idle"},
		{"own commits, idle just under limit", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.LastUsed = now.Add(-3*day + time.Minute) }, false, "idle"},
		{"unknown PR state falls through to idle", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.PRState = ""; f.LastUsed = now.Add(-5 * day) }, true, "idle"},
		{"grace beats everything", func(f *ephemeral.Facts) { f.Grace = 15 * time.Minute; f.OpenedAt = now.Add(-time.Minute) }, false, "grace"},
		{"grace expired", func(f *ephemeral.Facts) { f.Grace = 15 * time.Minute; f.OpenedAt = now.Add(-16 * time.Minute) }, true, "no commits of your own"},
		{"zero grace ignores a fresh worktree", func(f *ephemeral.Facts) { f.OpenedAt = now }, true, "no commits of your own"},
		{"zero opened-at is never done", func(f *ephemeral.Facts) { f.OpenedAt = time.Time{} }, false, "marker has no created_at"},
		{"zero opened-at beats grace and idle", func(f *ephemeral.Facts) {
			f.OpenedAt, f.Grace, f.OwnCommits, f.LastUsed = time.Time{}, 15*time.Minute, 1, time.Time{}
		}, false, "marker has no created_at"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := base
			c.edit(&f)
			v := ephemeral.Decide(f)
			if v.Done != c.done {
				t.Errorf("Done = %v, want %v (reason %q)", v.Done, c.done, v.Reason)
			}
			if !strings.Contains(v.Reason, c.contains) {
				t.Errorf("Reason = %q, want it to mention %q", v.Reason, c.contains)
			}
		})
	}
}

package ephemeral

import (
	"os"
	"path/filepath"
	"time"
)

// TriggerDue reports whether a background sweep is due, recording now as
// the last trigger when it is (spec §3.2). Two wt commands racing here can
// both see "due"; that only costs a second sweep, which exits on the sweep
// lock.
func TriggerDue(now time.Time, interval time.Duration) bool {
	p := filepath.Join(StateDir(), "last-sweep")
	if st, err := os.Stat(p); err == nil && now.Sub(st.ModTime()) < interval {
		return false
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return false
	}
	if os.WriteFile(p, nil, 0o600) != nil {
		return false
	}
	_ = os.Chtimes(p, now, now)
	return true
}

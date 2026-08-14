package ephemeral

import (
	"context"
	"os"
	"path/filepath"

	"github.com/theczechr/wt/internal/config"
)

// Sweep removes every ephemeral worktree that passes the reap predicate,
// returning the paths removed.
//
// This is not a duplicate of the SessionEnd hook but its backstop: the hook
// is not documented to run after a crash, a SIGKILL, or a reboot, and a
// worktree missed then would otherwise live forever. Both paths go through
// Reap, so both are bound by the same checks.
func Sweep(ctx context.Context, cfg config.Config, primaries map[string]string) []string {
	var removed []string
	for name, primary := range primaries {
		r, ok := cfg.Repos[name]
		if !ok || !r.EphemeralEnabled() {
			continue
		}
		root := filepath.Join(primary, r.EphemeralDirOrDefault())
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // no ephemeral dir yet is the normal case, not a failure
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name())
			if _, err := ReadMarker(path); err != nil {
				continue // not wt's to remove
			}
			// No session exemption: a sweep runs from a `wt` launch, not from
			// a session's own SessionEnd hook, so every live session it can
			// see is somebody else's and must block.
			if err := Reap(ctx, cfg, path, ""); err != nil {
				LogReap(path, err)
				continue
			}
			LogReap(path, nil)
			removed = append(removed, path)
		}
	}
	return removed
}

package ephemeral

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/theczechr/ephemera/internal/config"
)

// Sweep removes every ephemeral worktree that passes the reap predicate,
// returning one Decision per worktree it inspected (or, with DryRun, would
// have).
//
// This is not a duplicate of the SessionEnd hook but its backstop: the hook
// is not documented to run after a crash, a SIGKILL, or a reboot, and a
// worktree missed then would otherwise live forever. Both paths go through
// ReapWith, so both are bound by the same checks.
//
// SweepOptions tunes a sweep.
type SweepOptions struct {
	DryRun bool
	Grace  time.Duration
}

func Sweep(ctx context.Context, cfg config.Config, primaries map[string]string, opts SweepOptions) ([]Decision, error) {
	var out []Decision
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
			io := InspectOptions{Grace: opts.Grace}
			if opts.DryRun {
				out = append(out, Inspect(ctx, cfg, path, io))
				continue
			}
			d, err := ReapWith(ctx, cfg, path, io)
			LogReap(path, err)
			out = append(out, d)
		}
	}
	return out, nil
}

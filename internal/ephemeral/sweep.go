package ephemeral

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/theczechr/ephemera/internal/config"
)

// SweepOptions tunes a sweep.
type SweepOptions struct {
	DryRun bool
	Grace  time.Duration
}

// Sweep removes every ephemeral worktree that passes the reap predicate,
// returning one Decision per worktree it inspected (or, with DryRun, would
// have).
//
// This is not a duplicate of the SessionEnd hook but its backstop: the hook
// is not documented to run after a crash, a SIGKILL, or a reboot, and a
// worktree missed then would otherwise live forever. Both paths go through
// ReapWith, so both are bound by the same checks.
//
// Only one sweep runs at a time; a second returns ErrSweepBusy immediately
// (spec §3.4). Repos are walked in name order so dry-run output is stable.
func Sweep(ctx context.Context, cfg config.Config, primaries map[string]string, opts SweepOptions) ([]Decision, error) {
	release, ok, err := acquireLock(sweepLockName)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSweepBusy
	}
	defer release()

	names := make([]string, 0, len(primaries))
	for n := range primaries {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []Decision
	for _, name := range names {
		primary := primaries[name]
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
				// Not wt's to remove. A dry run says so, so a user asking
				// "why is this never cleaned up" gets an answer (spec §8);
				// a real sweep stays silent rather than log every stray dir
				// on every run.
				if opts.DryRun {
					out = append(out, unmarkedDecision(name, path, err))
				}
				continue
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
			LogReap(path, err, d.Reason)
			out = append(out, d)
		}
	}
	return out, nil
}

// unmarkedDecision is the dry-run verdict for a directory with no usable
// marker.
func unmarkedDecision(repo, path string, err error) Decision {
	reason := "no " + MarkerName + " marker: not created by wt; never removed automatically"
	if !errors.Is(err, os.ErrNotExist) {
		reason = fmt.Sprintf("marker unreadable: %v", err)
	}
	return Decision{Path: path, Repo: repo, Reason: reason}
}

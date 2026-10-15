package ephemeral

import (
	"context"
	"testing"

	"github.com/theczechr/ephemera/internal/model"
)

// SetPRStateLookupForTest swaps the gh-backed PR lookup for f and returns a
// function restoring the original. Tests must never reach the real gh.
func SetPRStateLookupForTest(f func(ctx context.Context, primary string, m Marker) (string, error)) func() {
	old := prStateLookup
	prStateLookup = f
	return func() { prStateLookup = old }
}

// SetBeforeRemovalForTest installs a hook run in ReapWith just before the
// final liveness re-probe and returns a function restoring the original.
func SetBeforeRemovalForTest(f func(worktree string)) (restore func()) {
	old := beforeRemoval
	beforeRemoval = f
	return func() { beforeRemoval = old }
}

// HoldSweepLockForTest takes the sweep lock as a separate open file, the
// way a second wt process would.
func HoldSweepLockForTest(t *testing.T) func() {
	release, ok, err := acquireLock(sweepLockName)
	if err != nil || !ok {
		t.Fatalf("could not take the sweep lock: ok=%v err=%v", ok, err)
	}
	return release
}

// SetAncestorChainForTest swaps the ordered (nearest-first) ancestor lookup
// the SessionEnd exemption uses and returns a function restoring the
// original.
func SetAncestorChainForTest(f func(ctx context.Context) ([]model.Proc, error)) (restore func()) {
	old := ancestorChain
	ancestorChain = f
	return func() { ancestorChain = old }
}

// LiveSessionPIDsForTest runs the liveness probe and returns the pids of the
// live sessions it found.
func LiveSessionPIDsForTest(ctx context.Context, worktree, excludeSessionID string) []int {
	sessions, _, _, _ := probeLiveness(ctx, worktree, excludeSessionID)
	var pids []int
	for _, s := range sessions {
		if s.Live {
			pids = append(pids, s.PID)
		}
	}
	return pids
}

// SetAfterRechecksForTest installs a hook run in ReapWith after the liveness
// and cleanliness rechecks and returns a function restoring the original.
func SetAfterRechecksForTest(f func(worktree string)) (restore func()) {
	old := afterRechecks
	afterRechecks = f
	return func() { afterRechecks = old }
}

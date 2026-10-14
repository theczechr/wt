package ephemeral

import (
	"context"
	"testing"
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

// SetAncestorPIDsForTest swaps the ancestor-process lookup the SessionEnd
// exemption uses and returns a function restoring the original.
func SetAncestorPIDsForTest(f func(ctx context.Context) (map[int]bool, error)) (restore func()) {
	old := ancestorPIDs
	ancestorPIDs = f
	return func() { ancestorPIDs = old }
}

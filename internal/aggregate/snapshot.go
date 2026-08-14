package aggregate

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/theczechr/wt/internal/discover"
	"github.com/theczechr/wt/internal/model"
)

// snapshotPath lives next to the PR and session caches, under the same
// WT_CACHE_DIR-overridable root, so tests stay hermetic.
func snapshotPath() string {
	return filepath.Join(discover.CacheRoot(), "snapshot.json")
}

// WriteSnapshot persists the full workspace list so the next TUI launch can
// paint immediately from it, before a live Collect finishes. Called by
// Collect itself after every successful run (both `wt status` and the bare
// TUI invocation collect, so both keep the snapshot fresh). Best-effort and
// atomic (temp file + rename): a reader must never observe a truncated
// file, and a write failure here (permissions, disk full, ...) must never
// fail the caller -- a snapshot is a pure UI optimisation, never a source
// of truth.
func WriteSnapshot(ws []model.Workspace) error {
	p := snapshotPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(ws)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".snapshot-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, writeErr := tmp.Write(body)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(tmpPath)
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	if err := os.Rename(tmpPath, p); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// LoadSnapshot reads the persisted workspace list. Any problem -- missing
// file, corrupt JSON -- yields (nil, false): a snapshot is never mistaken
// for a real Collect result, so every caller must treat a miss exactly like
// "no snapshot exists yet" and fall back to a live Collect.
func LoadSnapshot() ([]model.Workspace, bool) {
	body, err := os.ReadFile(snapshotPath())
	if err != nil {
		return nil, false
	}
	var ws []model.Workspace
	if json.Unmarshal(body, &ws) != nil {
		return nil, false
	}
	return ws, true
}

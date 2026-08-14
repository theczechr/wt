// Package trash implements the soft-delete trash manifest: worktrees
// removed with `d d` (soft mode) are recorded here so <space>t can list,
// restore, or expire them.
//
// It also holds the three destructive git operations this feature adds --
// SoftDelete, HardDelete and Restore, in delete.go -- so every place that
// can mutate a worktree or its branch lives in one small, auditable file.
package trash

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/theczechr/wt/internal/discover"
)

// Entry is one soft-deleted worktree recorded in the trash manifest.
// Primary is the repo's primary checkout path at the time of deletion,
// needed by Restore to run `git worktree add` and re-run bootstrap against
// the same primary the worktree originally came from.
type Entry struct {
	Path      string    `json:"path"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Primary   string    `json:"primary"`
	DeletedAt time.Time `json:"deleted_at"`
}

// Age returns a short, human-readable age string ("just now", "3h", "31d")
// relative to now.
func (e Entry) Age(now time.Time) string {
	d := now.Sub(e.DeletedAt)
	if days := int(d.Hours() / 24); days >= 1 {
		return fmt.Sprintf("%dd", days)
	}
	if hours := int(d.Hours()); hours >= 1 {
		return fmt.Sprintf("%dh", hours)
	}
	return "just now"
}

// manifestPath lives next to the PR, session and snapshot caches, under the
// same WT_CACHE_DIR-overridable root (discover.CacheRoot), so tests stay
// hermetic.
func manifestPath() string {
	return filepath.Join(discover.CacheRoot(), "trash.json")
}

// Load reads the trash manifest. Any problem -- missing file, corrupt JSON
// -- degrades to an empty trash, never an error: the directories these
// entries describe were already removed at soft-delete time, so a bad
// manifest can never cause data loss, only a forgotten "you can restore
// this" pointer.
func Load() []Entry {
	body, err := os.ReadFile(manifestPath())
	if err != nil {
		return nil
	}
	var entries []Entry
	if json.Unmarshal(body, &entries) != nil {
		return nil
	}
	return entries
}

// Save writes the manifest atomically (temp file + rename) -- the same
// pattern aggregate.WriteSnapshot and discover.SessionCache.Flush use, so a
// reader never observes a truncated file.
func Save(entries []Entry) error {
	p := manifestPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if entries == nil {
		entries = []Entry{}
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".trash-*.json.tmp")
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

// Add appends one entry to the manifest and persists it.
func Add(e Entry) error {
	entries := Load()
	entries = append(entries, e)
	return Save(entries)
}

// Remove drops the entry matching both Path and DeletedAt -- the pair that
// identifies one trashed worktree uniquely at any point in time, since a
// path can't be trashed twice concurrently (the first trashing already
// removed it from disk, and Restore refuses when the path is occupied
// again) -- and persists the result. Used by Restore on success and by the
// trash view's "D" purge.
func Remove(e Entry) error {
	entries := Load()
	out := make([]Entry, 0, len(entries))
	removed := false
	for _, x := range entries {
		if !removed && x.Path == e.Path && x.DeletedAt.Equal(e.DeletedAt) {
			removed = true
			continue
		}
		out = append(out, x)
	}
	return Save(out)
}

// PurgeExpired drops every manifest entry older than retention (relative to
// now) and persists the remainder, returning what was purged so the caller
// can report it.
//
// This only ever touches the manifest record. The worktree directory an
// entry describes was already removed by `git worktree remove` at
// soft-delete time (see SoftDelete in delete.go) -- expiry has nothing left
// to delete on disk and can only forget a now-stale "you can restore this"
// pointer, never lose work. That is exactly what makes it safe to run
// unconditionally, with no confirmation, on every launch.
func PurgeExpired(retention time.Duration, now time.Time) []Entry {
	all := Load()
	var kept, purged []Entry
	for _, e := range all {
		if now.Sub(e.DeletedAt) > retention {
			purged = append(purged, e)
		} else {
			kept = append(kept, e)
		}
	}
	if len(purged) > 0 {
		_ = Save(kept) // best-effort; a save failure just means these are re-evaluated next launch
	}
	return purged
}

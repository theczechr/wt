package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/theczechr/wt/internal/model"
)

// sessionCacheEntry is what's persisted per transcript: the parsed Session
// plus the file size it was parsed from. Session.Mtime is already the
// transcript's own mtime (ReadSessionTail sets it from os.Stat), so the
// (mtime, size) cache key is Session.Mtime paired with this Size -- no need
// to duplicate the mtime in a second field.
type sessionCacheEntry struct {
	Session model.Session
	Size    int64
}

// SessionCache is an in-memory, disk-backed cache of parsed session labels,
// keyed on absolute transcript path. It exists because up to several
// hundred transcript files, some hundreds of KB each, are tail-read and
// JSON-parsed by ReadSessionTail on every invocation of wt, and almost none
// of them change between two runs seconds apart.
//
// Safe for concurrent use from multiple goroutines: base is read-only after
// LoadSessionCache constructs it, and updates is guarded by mu. Every
// failure mode -- missing file, corrupt JSON, an unwritable cache dir --
// degrades to an empty cache or a skipped write, never an error: this is a
// pure optimisation, not a source of truth.
type SessionCache struct {
	mu      sync.Mutex
	base    map[string]sessionCacheEntry // loaded once; read-only thereafter
	updates map[string]sessionCacheEntry // staged by Put, merged in by Flush
}

// sessionCachePath is the fixed on-disk location, next to the PR cache and
// honouring the same WT_CACHE_DIR override (cacheRoot, defined in pr.go) so
// tests stay hermetic. Unlike the PR cache -- one file per repo -- there is
// only one session cache file, since sessions aren't scoped to a single
// repo the way PR state is.
func sessionCachePath() string {
	return filepath.Join(cacheRoot(), "sessions.json")
}

// LoadSessionCache reads the on-disk cache once. Any problem reading or
// parsing it -- missing file, corrupt JSON, a truncated write caught
// mid-flight -- yields an empty, working cache rather than an error, so a
// bad cache file can never break a run; every lookup just falls through to
// a live read.
func LoadSessionCache() *SessionCache {
	c := &SessionCache{base: map[string]sessionCacheEntry{}, updates: map[string]sessionCacheEntry{}}
	body, err := os.ReadFile(sessionCachePath())
	if err != nil {
		return c
	}
	var m map[string]sessionCacheEntry
	if json.Unmarshal(body, &m) != nil || m == nil {
		return c
	}
	c.base = m
	return c
}

// Get returns the cached session for path if the cache holds an entry whose
// recorded (mtime, size) matches the file's current identity. A nil
// receiver (no cache available) always misses, so callers can pass a nil
// *SessionCache and get today's live-read behaviour unconditionally.
func (c *SessionCache) Get(path string, mtime time.Time, size int64) (model.Session, bool) {
	if c == nil {
		return model.Session{}, false
	}
	c.mu.Lock()
	e, ok := c.updates[path]
	if !ok {
		e, ok = c.base[path]
	}
	c.mu.Unlock()
	if !ok || !e.Session.Mtime.Equal(mtime) || e.Size != size {
		return model.Session{}, false
	}
	return e.Session, true
}

// Put stages a freshly-parsed session for the next Flush. A nil receiver is
// a no-op.
func (c *SessionCache) Put(path string, s model.Session, size int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.updates[path] = sessionCacheEntry{Session: s, Size: size}
	c.mu.Unlock()
}

// Flush persists the cache to disk: the entries it was loaded with, overlaid
// with everything Put staged since, pruned to only the paths that still
// exist on disk -- transcripts get deleted independently of wt (Claude
// Code's own retention), and an entry for a gone file would just be dead
// weight. Written atomically (temp file + rename): the existing PR cache
// uses a plain os.WriteFile, and with concurrent readers that can expose a
// half-written file; a rename is atomic on the same filesystem, so a
// concurrent reader always sees either the old file or the complete new one.
// A nil receiver is a no-op. A write failure here is swallowed by the
// caller (aggregate.Collect) exactly like the PR cache's: caching is a pure
// optimisation and must never fail a Collect.
func (c *SessionCache) Flush() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	merged := make(map[string]sessionCacheEntry, len(c.base)+len(c.updates))
	for k, v := range c.base {
		merged[k] = v
	}
	for k, v := range c.updates {
		merged[k] = v
	}
	c.mu.Unlock()

	pruned := make(map[string]sessionCacheEntry, len(merged))
	for path, e := range merged {
		if _, err := os.Stat(path); err == nil {
			pruned[path] = e
		}
	}

	p := sessionCachePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(pruned)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".sessions-*.json.tmp")
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

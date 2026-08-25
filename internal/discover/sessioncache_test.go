package discover

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theczechr/wt/internal/model"
)

// writeTranscript writes a minimal transcript whose last ai-title is
// realTitle, and returns its path.
func writeTranscript(t *testing.T, dir, name, realTitle string) string {
	t.Helper()
	p := filepath.Join(dir, name+".jsonl")
	body := `{"type":"ai-title","aiTitle":"` + realTitle + `","sessionId":"` + name + `"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// setupProjectDir points $HOME at a temp dir and creates the
// ProjectDirName-flattened directory SessionsFor reads from for
// worktreePath, returning that directory.
func setupProjectDir(t *testing.T, worktreePath string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", model.ProjectDirName(worktreePath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSessionCacheHitReturnsCachedValueWithoutRereading(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	worktreePath := "/tmp/wtA"
	dir := setupProjectDir(t, worktreePath)
	p := writeTranscript(t, dir, "sess1", "the real title on disk")

	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	cache := LoadSessionCache()
	// Seed the cache with an entry keyed to the file's REAL (mtime, size)
	// but a Title that could only have come from the cache, never from a
	// fresh read of the file on disk.
	cache.Put(p, model.Session{ID: "sess1", Title: "cached title, not the real one", Mtime: st.ModTime()}, st.Size())
	if err := cache.Flush(); err != nil {
		t.Fatal(err)
	}

	// Fresh load, as a new wt invocation would do.
	cache2 := LoadSessionCache()
	sessions, _ := SessionsFor(worktreePath, cache2)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].Title != "cached title, not the real one" {
		t.Errorf("Title = %q, want the cached value (proves the cache hit, not a re-read)", sessions[0].Title)
	}
}

func TestSessionCacheChangedIdentityForcesReread(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	worktreePath := "/tmp/wtB"
	dir := setupProjectDir(t, worktreePath)
	p := writeTranscript(t, dir, "sess2", "the real current title")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	cache := LoadSessionCache()
	// Stage a cache entry under the right mtime but a WRONG size, so it can
	// never match the real file's identity.
	cache.Put(p, model.Session{ID: "sess2", Title: "stale cached title", Mtime: st.ModTime()}, 999999)
	if err := cache.Flush(); err != nil {
		t.Fatal(err)
	}

	cache2 := LoadSessionCache()
	sessions, _ := SessionsFor(worktreePath, cache2)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].Title != "the real current title" {
		t.Errorf("Title = %q, want a live re-read of the real content", sessions[0].Title)
	}
}

func TestSessionCacheCorruptFileDegradesToLiveRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WT_CACHE_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	worktreePath := "/tmp/wtC"
	pdir := setupProjectDir(t, worktreePath)
	writeTranscript(t, pdir, "sess3", "live title from disk")

	cache := LoadSessionCache()
	sessions, _ := SessionsFor(worktreePath, cache)
	if len(sessions) != 1 || sessions[0].Title != "live title from disk" {
		t.Errorf("corrupt cache must degrade to a live read, got %+v", sessions)
	}
}

func TestSessionCacheNilDegradesToLiveReadEveryTime(t *testing.T) {
	worktreePath := "/tmp/wtD"
	dir := setupProjectDir(t, worktreePath)
	writeTranscript(t, dir, "sess4", "always live")

	sessions, _ := SessionsFor(worktreePath, nil)
	if len(sessions) != 1 || sessions[0].Title != "always live" {
		t.Errorf("nil cache must behave like no caching at all, got %+v", sessions)
	}
}

func TestSessionCacheFlushPrunesDeletedFiles(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	dir := t.TempDir()
	p := writeTranscript(t, dir, "gone", "will be deleted")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	cache := LoadSessionCache()
	cache.Put(p, model.Session{ID: "gone", Mtime: st.ModTime()}, st.Size())
	if err := cache.Flush(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	// Loading again and flushing again (as the next wt run would, after
	// finding nothing new for this now-deleted transcript) must drop the
	// dead entry rather than carry it forward forever.
	cache2 := LoadSessionCache()
	if _, ok := cache2.Get(p, st.ModTime(), st.Size()); !ok {
		t.Fatal("setup: expected the deleted file's entry to still be loadable before the prune")
	}
	if err := cache2.Flush(); err != nil {
		t.Fatal(err)
	}
	cache3 := LoadSessionCache()
	if _, ok := cache3.Get(p, st.ModTime(), st.Size()); ok {
		t.Error("Flush must prune entries whose file no longer exists on disk")
	}
}

func TestSessionCacheGetMissWhenAbsent(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	cache := LoadSessionCache()
	if _, ok := cache.Get("/nowhere", time.Now(), 0); ok {
		t.Error("empty cache must miss")
	}
}

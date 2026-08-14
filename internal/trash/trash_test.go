package trash

import (
	"os"
	"testing"
	"time"
)

func TestAddLoadRoundTrip(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())

	e := Entry{Path: "/u/server-old", Repo: "backend", Branch: "fix/whatever", Primary: "/u/server", DeletedAt: time.Now()}
	if err := Add(e); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got) != 1 {
		t.Fatalf("Load() = %d entries, want 1", len(got))
	}
	if got[0].Path != e.Path || got[0].Branch != e.Branch || got[0].Repo != e.Repo {
		t.Errorf("got %+v, want %+v", got[0], e)
	}
}

func TestLoadMissingFileDegradesToEmpty(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	if got := Load(); len(got) != 0 {
		t.Errorf("Load() on a missing manifest = %v, want empty", got)
	}
}

func TestLoadCorruptFileDegradesToEmptyNeverPanics(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WT_CACHE_DIR", dir)
	if err := Save(nil); err != nil {
		t.Fatal(err)
	}
	// Overwrite with garbage after Save has created the directory.
	p := manifestPath()
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(); len(got) != 0 {
		t.Errorf("Load() on a corrupt manifest = %v, want empty, not a panic", got)
	}
}

func TestRemoveDropsOnlyTheMatchingEntry(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	now := time.Now()
	a := Entry{Path: "/u/a", Repo: "backend", Branch: "a", DeletedAt: now}
	b := Entry{Path: "/u/b", Repo: "backend", Branch: "b", DeletedAt: now.Add(time.Hour)}
	if err := Add(a); err != nil {
		t.Fatal(err)
	}
	if err := Add(b); err != nil {
		t.Fatal(err)
	}
	if err := Remove(a); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got) != 1 || got[0].Path != "/u/b" {
		t.Errorf("Remove(a) left %+v, want only b", got)
	}
}

func TestPurgeExpiredDropsOldKeepsFresh(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	now := time.Now()
	old := Entry{Path: "/u/old", Repo: "backend", Branch: "old", DeletedAt: now.Add(-31 * 24 * time.Hour)}
	fresh := Entry{Path: "/u/fresh", Repo: "backend", Branch: "fresh", DeletedAt: now.Add(-2 * 24 * time.Hour)}
	if err := Add(old); err != nil {
		t.Fatal(err)
	}
	if err := Add(fresh); err != nil {
		t.Fatal(err)
	}

	purged := PurgeExpired(30*24*time.Hour, now)
	if len(purged) != 1 || purged[0].Path != "/u/old" {
		t.Fatalf("PurgeExpired purged = %+v, want only the 31d-old entry", purged)
	}

	remaining := Load()
	if len(remaining) != 1 || remaining[0].Path != "/u/fresh" {
		t.Errorf("Load() after purge = %+v, want only the fresh entry kept", remaining)
	}
}

func TestPurgeExpiredNoneOverdueLeavesManifestUntouched(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	now := time.Now()
	fresh := Entry{Path: "/u/fresh", Repo: "backend", Branch: "fresh", DeletedAt: now.Add(-1 * time.Hour)}
	if err := Add(fresh); err != nil {
		t.Fatal(err)
	}
	purged := PurgeExpired(30*24*time.Hour, now)
	if len(purged) != 0 {
		t.Errorf("PurgeExpired purged = %+v, want none", purged)
	}
	if got := Load(); len(got) != 1 {
		t.Errorf("Load() after a no-op purge = %v, want the one entry still present", got)
	}
}

func TestEntryAgeFormatsDaysHoursAndJustNow(t *testing.T) {
	now := time.Now()
	cases := []struct {
		deletedAt time.Time
		want      string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-31 * 24 * time.Hour), "31d"},
	}
	for _, c := range cases {
		e := Entry{DeletedAt: c.deletedAt}
		if got := e.Age(now); got != c.want {
			t.Errorf("Age() = %q, want %q", got, c.want)
		}
	}
}

package aggregate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theczechr/wt/internal/model"
)

func TestSnapshotRoundTrip(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())

	in := []model.Workspace{
		{Repo: "backend", Path: "/u/server", Branch: "develop", Kind: model.KindPrimary},
		{Repo: "backend", Path: "/u/server-x", Branch: "feature/x", DirtyCount: 3, Kind: model.KindSibling},
	}
	if err := WriteSnapshot(in); err != nil {
		t.Fatal(err)
	}
	out, ok := LoadSnapshot()
	if !ok {
		t.Fatal("expected a snapshot to load after WriteSnapshot")
	}
	if len(out) != 2 || out[1].Path != "/u/server-x" || out[1].DirtyCount != 3 {
		t.Errorf("got %+v", out)
	}
}

func TestSnapshotMissingFileDegradesToMiss(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	if _, ok := LoadSnapshot(); ok {
		t.Error("no snapshot file must yield ok=false, not an error/panic")
	}
}

func TestSnapshotCorruptFileDegradesToMiss(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WT_CACHE_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadSnapshot(); ok {
		t.Error("a corrupt snapshot file must degrade to ok=false, never a panic or a bogus result")
	}
}

// TestSnapshotPreMigrationFileDeserializesToUnknownBlockedStatus documents
// the deliberate compatibility decision for this cache: a snapshot.json
// written before model.Workspace grew StatusKnown has no "StatusKnown" key
// at all. json.Unmarshal leaves an absent field at its Go zero value, so
// every entry loads with StatusKnown=false -- i.e. "unknown", which
// PruneBlockers refuses. That's the same fail-closed behaviour as any other
// zero-value Workspace: the TUI briefly shows those worktrees as
// undeletable ("?" in the dirty column, blocked in the "d d" popup) until
// the next live Collect (triggered automatically on launch, or via 'R')
// calls FillStatus and overwrites the snapshot with real StatusKnown
// values. No version bump or migration was added for this cache: a stale
// snapshot self-heals on the very next refresh, and blocking too eagerly
// for a few seconds is the safe direction to fail in, unlike the bug this
// change fixes.
func TestSnapshotPreMigrationFileDeserializesToUnknownBlockedStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WT_CACHE_DIR", dir)

	// Hand-authored JSON matching the pre-StatusKnown shape: a clean-looking
	// entry (DirtyCount 0, no dirty flag at all) with no "StatusKnown" key.
	preMigration := `[{"Repo":"backend","Path":"/u/server-x","Branch":"feature/x","Head":"abc123","DirtyCount":0,"Ahead":0,"Behind":0,"PR":{"Number":0,"State":""},"Kind":"sibling"}]`
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(preMigration), 0o644); err != nil {
		t.Fatal(err)
	}

	out, ok := LoadSnapshot()
	if !ok {
		t.Fatal("a pre-migration snapshot must still load, not degrade to a miss")
	}
	if len(out) != 1 {
		t.Fatalf("got %d workspaces, want 1", len(out))
	}
	w := out[0]
	if w.StatusKnown {
		t.Fatal("a pre-migration entry with no StatusKnown key must deserialize to false (unknown), not true")
	}
	blockers := w.PruneBlockers()
	if len(blockers) == 0 {
		t.Fatal("a pre-migration entry must be blocked from deletion until the next live refresh")
	}
}

func TestSnapshotEmptyWorkspacesRoundTrips(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	if err := WriteSnapshot(nil); err != nil {
		t.Fatal(err)
	}
	out, ok := LoadSnapshot()
	if !ok {
		t.Fatal("an empty-but-valid snapshot must still load")
	}
	if len(out) != 0 {
		t.Errorf("got %d workspaces, want 0", len(out))
	}
}

package aggregate

import (
	"path/filepath"
	"testing"

	"github.com/theczechr/wt/internal/model"
)

func TestDropFromSnapshotRemovesOnlyTheNamedWorktree(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	ws := []model.Workspace{
		{Repo: "server", Path: "/r/server", StatusKnown: true},
		{Repo: "server", Path: "/r/server-dqs", StatusKnown: true},
		// A sibling sharing a name prefix: it must survive.
		{Repo: "server", Path: "/r/server-dqsfix", StatusKnown: true},
	}
	if err := WriteSnapshot(ws); err != nil {
		t.Fatal(err)
	}

	dropped, err := DropFromSnapshot("/r/server-dqs")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Fatal("nothing was dropped")
	}
	got, ok := LoadSnapshot()
	if !ok {
		t.Fatal("snapshot unreadable after the drop")
	}
	if len(got) != 2 {
		t.Fatalf("snapshot has %d entries, want 2", len(got))
	}
	for _, w := range got {
		if w.Path == "/r/server-dqs" {
			t.Error("the removed worktree is still in the snapshot")
		}
	}
	found := false
	for _, w := range got {
		if w.Path == "/r/server-dqsfix" {
			found = true
		}
	}
	if !found {
		t.Error("a sibling sharing a name prefix was dropped too")
	}
}

// TestDropFromSnapshotIsQuietWithoutASnapshot: a removal hook must not fail
// just because nothing has been collected yet. There is simply nothing to
// prune, and re-deriving a snapshot inside a removal hook would mean running
// a full Collect in the wrong place.
func TestDropFromSnapshotIsQuietWithoutASnapshot(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", filepath.Join(t.TempDir(), "empty"))
	dropped, err := DropFromSnapshot("/r/whatever")
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if dropped {
		t.Error("reported a drop with no snapshot present")
	}
}

func TestDropFromSnapshotUnknownPathChangesNothing(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	ws := []model.Workspace{{Repo: "server", Path: "/r/server", StatusKnown: true}}
	if err := WriteSnapshot(ws); err != nil {
		t.Fatal(err)
	}
	dropped, err := DropFromSnapshot("/r/never-existed")
	if err != nil || dropped {
		t.Errorf("dropped=%v err=%v, want false/nil", dropped, err)
	}
	if got, _ := LoadSnapshot(); len(got) != 1 {
		t.Errorf("snapshot has %d entries, want 1 untouched", len(got))
	}
}

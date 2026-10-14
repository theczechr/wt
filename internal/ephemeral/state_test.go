package ephemeral_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestLogReapWritesFourColumns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WT_STATE_DIR", dir)
	ephemeral.LogReap("/w/a", nil, "no commits of your own")
	ephemeral.LogReap("/w/b", errors.New("refusing: dirty"), "has uncommitted changes")
	body, err := os.ReadFile(filepath.Join(dir, "reap.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	a := strings.Split(lines[0], "\t")
	if len(a) != 4 || a[1] != "/w/a" || a[2] != "reaped" || a[3] != "no commits of your own" {
		t.Errorf("line 1 = %q", lines[0])
	}
	b := strings.Split(lines[1], "\t")
	if len(b) != 4 || b[2] != "refusing: dirty" || b[3] != "has uncommitted changes" {
		t.Errorf("line 2 = %q", lines[1])
	}
}

func TestSweepIsExclusive(t *testing.T) {
	t.Setenv("WT_STATE_DIR", t.TempDir())
	r := gittest.New(t)
	cfg, _ := newEphemeral(t, r, "feat/lock")
	release := ephemeral.HoldSweepLockForTest(t)
	defer release()
	_, err := ephemeral.Sweep(context.Background(), cfg, map[string]string{"demo": r.Primary}, ephemeral.SweepOptions{})
	if !errors.Is(err, ephemeral.ErrSweepBusy) {
		t.Fatalf("err = %v, want ErrSweepBusy while another sweep holds the lock", err)
	}
}

func TestSweepDryRunDeletesNothing(t *testing.T) {
	t.Setenv("WT_STATE_DIR", t.TempDir())
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/dry")
	ds, err := ephemeral.Sweep(context.Background(), cfg, map[string]string{"demo": r.Primary}, ephemeral.SweepOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || !ds[0].Reap {
		t.Fatalf("decisions = %+v, want one 'would reap'", ds)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Error("dry run must not remove anything:", err)
	}
}

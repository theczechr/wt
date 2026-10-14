package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
)

// ephemeralFixture creates a clean, pushed, marked worktree whose marker is
// old enough to be past the sweep grace.
func ephemeralFixture(t *testing.T) (config.Config, string) {
	t.Helper()
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	t.Setenv("WT_STATE_DIR", t.TempDir())
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/old")
	cfg := cfgFor(r, config.Repo{})
	path, err := openBranch(cfg, "feat/old")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := ephemeral.UpdateMarker(path, func(m *ephemeral.Marker) { m.CreatedAt = old; m.LastOpenedAt = old }); err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func TestReapAllDryRunJSONReportsAndKeeps(t *testing.T) {
	cfg, path := ephemeralFixture(t)
	var out, errb bytes.Buffer
	code := runReap(cfg, []string{"--all", "--dry-run", "--json"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	var ds []ephemeral.Decision
	if err := json.Unmarshal(out.Bytes(), &ds); err != nil {
		t.Fatalf("stdout is not a JSON decision list: %v\n%s", err, out.String())
	}
	if len(ds) != 1 || ds[0].Path != path || !ds[0].Reap {
		t.Fatalf("decisions = %+v", ds)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("dry run removed the worktree")
	}
}

func TestReapAllRemovesAndPrintsReason(t *testing.T) {
	cfg, path := ephemeralFixture(t)
	var out, errb bytes.Buffer
	if code := runReap(cfg, []string{"--all"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "reaped") || !strings.Contains(out.String(), filepath.Base(path)) {
		t.Errorf("stdout = %q", out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the worktree must be gone")
	}
}

func TestReapAllEmptyJSONIsAnArray(t *testing.T) {
	t.Setenv("WT_STATE_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := runReap(config.Config{}, []string{"--all", "--json"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("stdout = %q, want []", out.String())
	}
}

func TestReapRejectsPathWithAll(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runReap(config.Config{}, []string{"--all", "/x"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Errorf("exit %d, want 2 for a usage error", code)
	}
}

package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestOpenBranchRecordsStartAndWTCreatedBranch(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/start")
	path, err := openBranch(cfgFor(r, config.Repo{}), "feat/start")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ephemeral.ReadMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(r.Git(t, path, "rev-parse", "HEAD")); m.Start != want {
		t.Errorf("Start = %q, want the worktree's HEAD %q", m.Start, want)
	}
	if !m.BranchCreatedByWT {
		t.Error("a remote-only branch gets a local copy created by wt; BranchCreatedByWT must be true")
	}
}

func TestOpenBranchOnExistingLocalBranchIsNotWTCreated(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.Git(t, r.Primary, "branch", "feat/local")
	path, err := openBranch(cfgFor(r, config.Repo{}), "feat/local")
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := ephemeral.ReadMarker(path); m.BranchCreatedByWT {
		t.Error("the user's own local branch must never be marked wt-created")
	}
}

func TestHandOffTouchesAnEphemeralMarker(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir()) // writeChoice target
	t.Setenv("HERDR_PLUGIN_ENTRYPOINT_ID", "")
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/touch")
	path, err := openBranch(cfgFor(r, config.Repo{}), "feat/touch")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	handOff(path, "")
	m, _ := ephemeral.ReadMarker(path)
	if m.LastOpenedAt.Before(before) {
		t.Errorf("LastOpenedAt = %v, want >= %v", m.LastOpenedAt, before)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

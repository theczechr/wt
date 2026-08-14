package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/bootstrap"
	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/gittest"
	"github.com/theczechr/wt/internal/model"
)

// TestNonNilWorkspacesEncodesEmptyArrayNotNull covers `wt status`'s
// documented scripting contract: encoding/json marshals a nil slice as the
// JSON literal `null`, which breaks a naive `jq '.[]'` when there are no
// workspaces to report (e.g. no configured repos found on disk).
// nonNilWorkspaces must normalise nil to an empty, non-nil slice so the
// output is always a JSON array.
func TestNonNilWorkspacesEncodesEmptyArrayNotNull(t *testing.T) {
	var nilWs []model.Workspace
	got := nonNilWorkspaces(nilWs)
	if got == nil {
		t.Fatal("nonNilWorkspaces(nil) must not return nil")
	}
	if len(got) != 0 {
		t.Fatalf("nonNilWorkspaces(nil) = %v, want empty", got)
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(got); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "[]\n" {
		t.Errorf("encoded = %q, want %q", buf.String(), "[]\n")
	}
}

// TestNonNilWorkspacesPassesThroughNonNil asserts the normal case is
// untouched: a populated slice is returned as-is.
func TestNonNilWorkspacesPassesThroughNonNil(t *testing.T) {
	ws := []model.Workspace{{Repo: "backend"}}
	got := nonNilWorkspaces(ws)
	if len(got) != 1 || got[0].Repo != "backend" {
		t.Errorf("got %v, want the original slice untouched", got)
	}
}

// TestIsReservedCommand covers both directions: every reserved word must be
// caught, and a branch name that merely resembles one (shares a prefix, or
// contains one as a substring) must not be swallowed as a subcommand -- that
// is exactly what `wt open <branch>` exists to escape.
func TestIsReservedCommand(t *testing.T) {
	for _, r := range []string{"status", "new", "bootstrap", "reap", "open", "help", "-h", "--help"} {
		if !isReservedCommand(r) {
			t.Errorf("%q must be reserved", r)
		}
	}
	for _, b := range []string{"feat/new", "main", "fix/status-bar", "newish"} {
		if isReservedCommand(b) {
			t.Errorf("%q must be treated as a branch name", b)
		}
	}
}

// TestHelpGoesToStdoutAndExitsZero pins the routing of an explicit help
// request. `help`, `-h` and `--help` are in reservedCommands, and reserved
// words with no case of their own fall to the switch's failure arm -- so they
// printed usage to stderr and exited 2, as if the user had made a mistake.
// Asking for help is not a usage error: `wt --help | less` must see the text
// on stdout, and `wt --help && ...` must not stop there.
//
// This shells out to a real binary because the behaviour under test is
// main()'s dispatch plus its exit code, and neither is reachable in-process
// without an os.Exit that would take the test binary down with it.
func TestHelpGoesToStdoutAndExitsZero(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building wt: %v: %s", err, out)
	}
	for _, arg := range []string{"help", "-h", "--help"} {
		cmd := exec.Command(bin, arg)
		// A throwaway HOME so config.DefaultPath() cannot pick up the
		// developer's real config.toml and fail the run for unrelated reasons.
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Errorf("wt %s: want exit 0, got %v (stderr: %s)", arg, err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "usage:") {
			t.Errorf("wt %s: usage must go to stdout, got stdout=%q stderr=%q",
				arg, stdout.String(), stderr.String())
		}
	}
}

// cfgFor builds the minimal config.Config that lets resolve.Primaries find
// r's primary checkout: the repo map key must equal the primary's basename,
// since that's what Primaries joins against each scan root.
func cfgFor(r *gittest.Repo, repo config.Repo) config.Config {
	name := filepath.Base(r.Primary)
	return config.Config{
		ScanRoots: []string{filepath.Dir(r.Primary)},
		Repos:     map[string]config.Repo{name: repo},
	}
}

// TestOpenBranchRemoteOnlyBranchChecksOutTheRemoteCode pins the sole call
// site of the empty-start-point fix an earlier task exists to deliver:
// openBranch's `startPoint` computation. A branch that exists only upstream
// must be created FROM the remote ref, not from the primary's HEAD --
// bootstrap.CreateAt's own doc comment says passing "" for a remote-only
// branch silently produces an empty branch instead of the PR's code. Nothing
// else in the suite reaches this arm of openBranch, so a future regression
// (e.g. `startPoint := ""` left unconditional) would otherwise go silent:
// the user gets a worktree that looks right and contains the wrong code.
func TestOpenBranchRemoteOnlyBranchChecksOutTheRemoteCode(t *testing.T) {
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/remote-thing")
	cfg := cfgFor(r, config.Repo{})

	got, err := openBranch(cfg, "feat/remote-thing")
	if err != nil {
		t.Fatalf("openBranch: %v", err)
	}

	want := filepath.Join(r.Primary, ".worktrees", bootstrap.Slug("feat/remote-thing"))
	if got != want {
		t.Fatalf("openBranch path = %q, want %q", got, want)
	}

	worktreeHead := strings.TrimSpace(r.Git(t, got, "rev-parse", "HEAD"))
	remoteHead := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "origin/feat/remote-thing"))
	primaryHead := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "HEAD"))

	// Both assertions matter: the first alone would pass if the remote and
	// primary HEADs happened to coincide (they never do here, since
	// PushNewBranch always adds a commit, but the point of the second
	// assertion is to not depend on that).
	if worktreeHead != remoteHead {
		t.Errorf("worktree HEAD = %s, want the remote branch's HEAD %s", worktreeHead, remoteHead)
	}
	if worktreeHead == primaryHead {
		t.Errorf("worktree HEAD = %s, must not equal the primary's HEAD %s (would mean it branched from HEAD, not the remote)", worktreeHead, primaryHead)
	}

	if _, err := os.Stat(filepath.Join(got, ".wt-ephemeral")); err != nil {
		t.Errorf(".wt-ephemeral marker missing: %v", err)
	}
}

// TestOpenBranchWritesNoMarkerWhenBootstrapFails pins the other ordering
// constraint no other test can reach: ephemeral.WriteMarker must run only
// after bootstrap.CreateAt succeeds. A failing post_create hook leaves the
// worktree on disk (CreateAt's own contract) but must NOT leave a marker,
// or the reaper would eventually delete a worktree the user never got a
// working copy of and was never told existed.
func TestOpenBranchWritesNoMarkerWhenBootstrapFails(t *testing.T) {
	r := gittest.New(t)
	r.Git(t, r.Primary, "branch", "feat/broken")
	cfg := cfgFor(r, config.Repo{PostCreate: []string{"exit 1"}})

	_, err := openBranch(cfg, "feat/broken")
	if err == nil {
		t.Fatal("openBranch: want an error from the failing post_create hook, got nil")
	}
	if !strings.Contains(err.Error(), "left in place") {
		t.Errorf("error = %q, want it to say the worktree was left in place", err.Error())
	}

	target := filepath.Join(r.Primary, ".worktrees", bootstrap.Slug("feat/broken"))
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("worktree dir must still exist for inspection: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(target, ".wt-ephemeral")); !os.IsNotExist(statErr) {
		t.Errorf(".wt-ephemeral must be absent after a failed bootstrap, stat err = %v", statErr)
	}
}

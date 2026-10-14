package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestParsePRArg(t *testing.T) {
	cases := map[string]prRef{
		"1234":                                   {Number: 1234},
		"#1234":                                  {Number: 1234},
		"https://github.com/acme/server/pull/77": {Owner: "acme", Name: "server", Number: 77},
		"https://github.com/acme/server/pull/77/files": {Owner: "acme", Name: "server", Number: 77},
	}
	for arg, want := range cases {
		got, ok := parsePRArg(arg)
		if !ok || got != want {
			t.Errorf("parsePRArg(%q) = %+v, %v; want %+v", arg, got, ok, want)
		}
	}
	for _, arg := range []string{"feat/1234", "12a", "#", "https://gitlab.com/a/b/-/merge_requests/1"} {
		if _, ok := parsePRArg(arg); ok {
			t.Errorf("parsePRArg(%q) must not be a PR", arg)
		}
	}
}

func TestOriginMatches(t *testing.T) {
	for _, u := range []string{
		"git@github.com:acme/server.git", "https://github.com/acme/server", "https://github.com/Acme/Server.git/",
	} {
		if !originMatches(u, "acme", "server") {
			t.Errorf("%q must match acme/server", u)
		}
	}
	if originMatches("git@github.com:acme/server-old.git", "acme", "server") {
		t.Error("a different repo with a shared prefix must not match")
	}
}

// fakeGH puts a `gh` first on PATH that prints body for any arguments.
func fakeGH(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + body + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// forkPR publishes a commit only as refs/pull/<n>/head on origin.
func forkPR(t *testing.T, r *gittest.Repo, n int, file string) string {
	t.Helper()
	r.Git(t, r.Origin, "checkout", "-q", "-b", "tmp-fork")
	r.WriteCommit(t, r.Origin, file, "x")
	sha := strings.TrimSpace(r.Git(t, r.Origin, "rev-parse", "HEAD"))
	r.Git(t, r.Origin, "update-ref", fmt.Sprintf("refs/pull/%d/head", n), sha)
	r.Git(t, r.Origin, "checkout", "-q", "main")
	r.Git(t, r.Origin, "branch", "-qD", "tmp-fork")
	return sha
}

func ghJSON(n int, head, oid string, fork bool) string {
	return fmt.Sprintf(`{"number":%d,"state":"OPEN","headRefName":%q,"headRefOid":%q,"isCrossRepository":%v,"url":"https://github.com/acme/demo/pull/%d"}`,
		n, head, oid, fork, n)
}

func TestOpenPRFromAFork(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	sha := forkPR(t, r, 7, "fork.txt")
	fakeGH(t, ghJSON(7, "main", sha, true))
	path, err := openPR(cfgFor(r, config.Repo{}), prRef{Number: 7}, r.Primary, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "pr-7" {
		t.Errorf("path = %s, want …/.worktrees/pr-7", path)
	}
	if got := strings.TrimSpace(r.Git(t, path, "rev-parse", "HEAD")); got != sha {
		t.Errorf("HEAD = %s, want the PR head %s", got, sha)
	}
	m, err := ephemeral.ReadMarker(path)
	if err != nil || m.PR != 7 || !m.BranchCreatedByWT || m.Branch != "pr/7" {
		t.Errorf("marker = %+v, err = %v", m, err)
	}
	if n := strings.TrimSpace(r.Git(t, r.Primary, "rev-list", "--count", "pr/7", "--not", "--remotes")); n != "0" {
		t.Errorf("pr/7 has %s commits outside refs/remotes; the PR head must be under refs/remotes/wt-pr", n)
	}
}

func TestOpenPRFromTheSameRepo(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.PushNewBranch(t, "feat/samerepo")
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	oid := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "origin/feat/samerepo"))
	fakeGH(t, ghJSON(8, "feat/samerepo", oid, false))
	path, err := openPR(cfgFor(r, config.Repo{}), prRef{Number: 8}, r.Primary, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := ephemeral.ReadMarker(path); m.PR != 8 || m.Branch != "feat/samerepo" {
		t.Errorf("marker = %+v, want PR 8 on feat/samerepo", m)
	}
}

func TestOpenPRAgainFastForwardsAReviewWorktree(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	sha := forkPR(t, r, 7, "one.txt")
	fakeGH(t, ghJSON(7, "main", sha, true))
	cfg := cfgFor(r, config.Repo{})
	path, err := openPR(cfg, prRef{Number: 7}, r.Primary, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	// The author pushes again.
	r.Git(t, r.Origin, "checkout", "-q", "-b", "tmp2", sha)
	r.WriteCommit(t, r.Origin, "two.txt", "y")
	sha2 := strings.TrimSpace(r.Git(t, r.Origin, "rev-parse", "HEAD"))
	r.Git(t, r.Origin, "update-ref", "refs/pull/7/head", sha2)
	r.Git(t, r.Origin, "checkout", "-q", "main")
	fakeGH(t, ghJSON(7, "main", sha2, true))
	if _, err := openPR(cfg, prRef{Number: 7}, r.Primary, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(r.Git(t, path, "rev-parse", "HEAD")); got != sha2 {
		t.Errorf("HEAD = %s, want fast-forwarded to %s", got, sha2)
	}
}

func TestOpenPRAgainLeavesLocalChangesAlone(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	sha := forkPR(t, r, 7, "one.txt")
	fakeGH(t, ghJSON(7, "main", sha, true))
	cfg := cfgFor(r, config.Repo{})
	path, _ := openPR(cfg, prRef{Number: 7}, r.Primary, &bytes.Buffer{})
	if err := os.WriteFile(filepath.Join(path, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Git(t, r.Origin, "checkout", "-q", "-b", "tmp2", sha)
	r.WriteCommit(t, r.Origin, "two.txt", "y")
	sha2 := strings.TrimSpace(r.Git(t, r.Origin, "rev-parse", "HEAD"))
	r.Git(t, r.Origin, "update-ref", "refs/pull/7/head", sha2)
	r.Git(t, r.Origin, "checkout", "-q", "main")
	fakeGH(t, ghJSON(7, "main", sha2, true))
	var errb bytes.Buffer
	if _, err := openPR(cfg, prRef{Number: 7}, r.Primary, &errb); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(r.Git(t, path, "rev-parse", "HEAD")); got != sha {
		t.Errorf("HEAD moved to %s; a worktree with local changes must be left alone", got)
	}
	if !strings.Contains(errb.String(), "1 new commit") {
		t.Errorf("stderr = %q, want it to say the PR has 1 new commit", errb.String())
	}
}

func TestOpenPRWithoutGHSaysSo(t *testing.T) {
	r := gittest.New(t)
	// A PATH holding git (repo resolution needs it) but no gh.
	bin := t.TempDir()
	gitPath, lerr := exec.LookPath("git")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	_, err := openPR(cfgFor(r, config.Repo{}), prRef{Number: 7}, r.Primary, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "GitHub CLI") {
		t.Fatalf("err = %v, want a pointer to the GitHub CLI", err)
	}
}

func TestOpenPROutsideAnyRepoWithoutTTYIsAUsageError(t *testing.T) {
	r := gittest.New(t)
	_, err := openPR(cfgFor(r, config.Repo{}), prRef{Number: 7}, t.TempDir(), &bytes.Buffer{})
	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want a usageError (exit 2) listing repos", err)
	}
}

func TestOpenStillOpensANumericBranch(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.Git(t, r.Primary, "branch", "1234")
	path, err := openBranch(cfgFor(r, config.Repo{}), "1234")
	if err != nil {
		t.Fatalf("`wt open 1234` must still reach a branch named 1234: %v", err)
	}
	if b := strings.TrimSpace(r.Git(t, path, "symbolic-ref", "--short", "HEAD")); b != "1234" {
		t.Errorf("checked out %q, want 1234", b)
	}
}

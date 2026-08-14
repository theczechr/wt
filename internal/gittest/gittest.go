// Package gittest builds throwaway git repositories for tests that need real
// git semantics rather than parsed fixture strings. The reap predicate is the
// motivating case: it gates deletion on upstream existence, ahead counts, and
// status success, none of which can be tested honestly against canned output.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is an origin repository plus a clone of it standing in for a primary
// checkout.
type Repo struct {
	Origin  string
	Primary string
}

// New creates an origin with one commit on branch "main" and clones it. Both
// live under t.TempDir(), so they are removed when the test finishes.
func New(t *testing.T) *Repo {
	t.Helper()
	base := t.TempDir()
	r := &Repo{
		Origin:  filepath.Join(base, "origin"),
		Primary: filepath.Join(base, "primary"),
	}
	if err := os.MkdirAll(r.Origin, 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git(t, r.Origin, "init", "-q", "-b", "main")
	r.configure(t, r.Origin)
	if err := os.WriteFile(filepath.Join(r.Origin, "README"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Git(t, r.Origin, "add", "-A")
	r.Git(t, r.Origin, "commit", "-qm", "seed")
	// A non-bare origin refuses a push to its checked-out branch; tests push
	// only to other branches, and receive.denyCurrentBranch would still fire
	// on a same-name push, so make it explicit rather than surprising.
	r.Git(t, r.Origin, "config", "receive.denyCurrentBranch", "ignore")
	r.Git(t, base, "clone", "-q", r.Origin, r.Primary)
	r.configure(t, r.Primary)
	return r
}

func (r *Repo) configure(t *testing.T, dir string) {
	t.Helper()
	r.Git(t, dir, "config", "user.email", "wt@test.invalid")
	r.Git(t, dir, "config", "user.name", "wt test")
	r.Git(t, dir, "config", "commit.gpgsign", "false")
}

// Git runs a git command and fails the test if it errors.
func (r *Repo) Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := r.TryGit(dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// TryGit runs a git command and returns its combined output and error, for
// tests asserting that a command fails.
func (r *Repo) TryGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Keep the ambient environment from steering these repos: a user's
	// global hooksPath, template dir, or commit signing config would
	// otherwise leak in and make results machine-dependent.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// WriteCommit writes a file in dir and commits it there.
func (r *Repo) WriteCommit(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Git(t, dir, "add", "-A")
	r.Git(t, dir, "commit", "-qm", "add "+name)
}

// PushNewBranch creates branch in the primary, pushes it to origin, and
// deletes the local ref -- leaving exactly the state of a PR branch someone
// else pushed: present as refs/remotes/origin/<branch>, absent locally.
func (r *Repo) PushNewBranch(t *testing.T, branch string) {
	t.Helper()
	start := strings.TrimSpace(r.Git(t, r.Primary, "rev-parse", "--abbrev-ref", "HEAD"))
	r.Git(t, r.Primary, "checkout", "-q", "-b", branch)
	r.WriteCommit(t, r.Primary, "on-"+strings.ReplaceAll(branch, "/", "-")+".txt", "x")
	r.Git(t, r.Primary, "push", "-q", "origin", branch)
	r.Git(t, r.Primary, "checkout", "-q", start)
	r.Git(t, r.Primary, "branch", "-qD", branch)
}

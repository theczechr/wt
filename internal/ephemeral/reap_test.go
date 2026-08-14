package ephemeral_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/discover"
	"github.com/theczechr/wt/internal/ephemeral"
	"github.com/theczechr/wt/internal/gittest"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/trash"
)

// blockersContain reports whether any blocker message contains substr. Tests
// use this instead of a bare len(b) != 0 check whenever more than one clause
// of the predicate could plausibly fire for the same fixture: a loose
// len-only assertion stays green even if the specific guard the test claims
// to cover is deleted, so long as some other, unrelated guard also happens
// to fire.
func blockersContain(b []string, substr string) bool {
	return blockerContaining(b, substr) != ""
}

// blockerContaining returns the first blocker containing substr, or "".
// Needed wherever a test asserts something about the CONTENT of one specific
// blocker: several clauses can fire on the same fixture, so a match anywhere
// in the slice proves nothing about the clause under test.
func blockerContaining(b []string, substr string) string {
	for _, s := range b {
		if strings.Contains(s, substr) {
			return s
		}
	}
	return ""
}

// newEphemeral builds the happy path: a clean, pushed, marked worktree under
// the primary's .worktrees dir. Each test then breaks exactly one property.
//
// WT_CACHE_DIR is redirected per-test because Reap writes a trash manifest
// through discover.CacheRoot; without this the suite would read and write the
// developer's real ~/.cache/wt/trash.json.
func newEphemeral(t *testing.T, r *gittest.Repo, branch string) (config.Config, string) {
	t.Helper()
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	wt := filepath.Join(r.Primary, ".worktrees", strings.ReplaceAll(branch, "/", "-"))
	r.PushNewBranch(t, branch)
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", branch, wt, "origin/"+branch)
	if err := ephemeral.EnsureExcluded(context.Background(), r.Primary,
		[]string{".worktrees/", ephemeral.MarkerName}); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.WriteMarker(wt, ephemeral.Marker{
		Repo: "demo", Branch: branch, Primary: r.Primary, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	return cfg, wt
}

func TestBlockersEmptyForACleanPushedMarkedWorktree(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/clean")
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) != 0 {
		t.Fatalf("want no blockers, got %v", b)
	}
}

func TestBlockersRefusesDirtyWorktree(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/dirty")
	if err := os.WriteFile(filepath.Join(wt, "README"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) == 0 {
		t.Fatal("a modified tracked file must block removal")
	}
}

func TestBlockersRefusesUntrackedFile(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/untracked")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) == 0 {
		t.Fatal("an untracked file must block removal")
	}
}

// The trap from the spec: no upstream yields Ahead == 0, identical to a
// fully-pushed branch, while being the most unpushed state possible.
func TestBlockersRefusesBranchWithNoUpstream(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/no-upstream")
	r.Git(t, wt, "branch", "--unset-upstream")
	r.WriteCommit(t, wt, "local-only.txt", "work that exists nowhere else")
	b := ephemeral.Blockers(context.Background(), cfg, wt)
	if len(b) == 0 {
		t.Fatal("a branch with no upstream must never be reaped: every commit is unpushed")
	}
}

func TestBlockersRefusesUnpushedCommits(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/ahead")
	r.WriteCommit(t, wt, "ahead.txt", "not pushed")
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) == 0 {
		t.Fatal("unpushed commits must block removal")
	}
}

func TestBlockersRefusesWhenStatusFails(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/broken-status")
	// Corrupting the worktree's gitdir pointer makes every git command in it
	// fail. A failed status must read as "not clean", never as "clean".
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The corrupted gitdir also breaks `rev-parse @{u}`, which fires its own
	// (unrelated) "no upstream" blocker -- so this asserts the SPECIFIC
	// status-read blocker, not just a non-empty slice, or this test would
	// stay green even with dirtyCount's error handling deleted entirely.
	b := ephemeral.Blockers(context.Background(), cfg, wt)
	status := blockerContaining(b, "could not read git status")
	if status == "" {
		t.Fatalf("a failed git status must produce a 'could not read git status' blocker, got %v", b)
	}
	// exec.Cmd.Output() leaves the error text at a bare "exit status 128" and
	// hides git's own diagnostic in ExitError.Stderr. That diagnostic is the
	// entire content of the refusal an operator reads back out of reap.log,
	// and the spec names a real worktree in this checkout whose status fails
	// outright because of a submodule misconfiguration -- an
	// exit status alone cannot be told apart from a corrupt gitdir or a stale
	// index.lock. Asserting git's own "fatal:" prefix rather than the full
	// message keeps this from breaking on a git release that rewords it.
	//
	// Asserted against THIS blocker's own text, not the slice: the corrupt
	// gitdir also fails `symbolic-ref`, whose blocker carries the same
	// "fatal:" -- so a blockersContain over the whole slice would stay green
	// with dirtyCount's stderr wrapping deleted.
	if !strings.Contains(status, "fatal:") {
		t.Errorf("the status blocker must carry git's own stderr, not just an exit status: %q", status)
	}
}

func TestBlockersRefusesWithoutMarker(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/unmarked")
	if err := os.Remove(filepath.Join(wt, ephemeral.MarkerName)); err != nil {
		t.Fatal(err)
	}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) == 0 {
		t.Fatal("removing the marker must promote the worktree to permanent")
	}
}

func TestBlockersRefusesWorktreeOutsideEphemeralDir(t *testing.T) {
	r := gittest.New(t)
	outside := filepath.Join(filepath.Dir(r.Primary), "hand-made")
	r.PushNewBranch(t, "feat/outside")
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/outside", outside, "origin/feat/outside")
	if err := ephemeral.WriteMarker(outside, ephemeral.Marker{
		Repo: "demo", Branch: "feat/outside", Primary: r.Primary, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	if b := ephemeral.Blockers(context.Background(), cfg, outside); len(b) == 0 {
		t.Fatal("a marker alone must not authorise removal outside the ephemeral dir")
	}
}

func TestBlockersRefusesThePrimaryCheckout(t *testing.T) {
	r := gittest.New(t)
	if err := ephemeral.WriteMarker(r.Primary, ephemeral.Marker{
		Repo: "demo", Branch: "main", Primary: r.Primary, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	// The primary also fails containment (it isn't inside its own
	// .worktrees dir) and, since this test never calls EnsureExcluded, shows
	// the marker file itself as an untracked change -- three clauses fire at
	// once. Assert the SPECIFIC primary-checkout blocker, or this test would
	// stay green even with that guard deleted outright.
	b := ephemeral.Blockers(context.Background(), cfg, r.Primary)
	if !blockersContain(b, "refusing to remove the primary checkout") {
		t.Fatalf("the primary checkout must produce a 'refusing to remove the primary checkout' blocker, got %v", b)
	}
}

func TestBlockersRefusesUnconfiguredRepo(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/unconfigured")
	cfg.Repos = map[string]config.Repo{}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) == 0 {
		t.Fatal("an unconfigured repo must block removal")
	}
}

// TestBlockersRefusesUnregisteredWorktree isolates the registered-worktree
// clause: a directory that is clean, pushed, correctly placed, and marked --
// every other clause passes -- but was built with a plain `git clone`
// instead of `git worktree add` against the primary, so git itself does not
// consider it one of the primary's worktrees. Only the registration check
// can catch this: a stale directory left behind after someone manually rm
// -rf'd a worktree without telling git looks exactly like this to every
// other clause.
func TestBlockersRefusesUnregisteredWorktree(t *testing.T) {
	r := gittest.New(t)
	branch := "feat/hand-made"
	r.PushNewBranch(t, branch)
	wt := filepath.Join(r.Primary, ".worktrees", strings.ReplaceAll(branch, "/", "-"))
	r.Git(t, r.Primary, "clone", "-q", "-b", branch, r.Origin, wt)
	if err := ephemeral.WriteMarker(wt, ephemeral.Marker{
		Repo: "demo", Branch: branch, Primary: r.Primary, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Repos: map[string]config.Repo{"demo": {Name: "demo"}}}
	b := ephemeral.Blockers(context.Background(), cfg, wt)
	if !blockersContain(b, "not a registered worktree") {
		t.Fatalf("a directory git never registered as a worktree must produce a 'not a registered worktree' blocker, got %v", b)
	}
}

// TestBlockersRefusesRunningProcess starts a real, long-lived child whose
// cwd is the worktree and asserts the running-process clause refuses.
//
// This is also the regression test for two review findings: discover.Snapshot
// used to swallow a failed `ps` as "nothing running" rather than reporting
// it (Critical 1, covered separately by
// TestBlockersRefusesWhenProcessEnumerationFails since it needs a
// deterministic way to make ps itself fail), and the cwd comparison used to
// compare raw strings instead of resolved ones (Critical 2). lsof reports
// the resolved vnode path -- on macOS, /private/var/folders/... for a cwd
// under t.TempDir()'s /var/folders/... symlink -- so an unresolved
// comparison would silently never match here, and this test would pass for
// the wrong reason (no process detected) rather than the right one (process
// detected, clause refuses).
func TestBlockersRefusesRunningProcess(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/has-process")

	// "go run" so discover's interesting() keyword filter treats this as
	// plausible work worth resolving a cwd for. Deliberately not node/deno/
	// etc: none of those are a dependency of this project, so on a machine
	// or CI image without them this test would silently skip and the only
	// in-repo regression coverage for Critical 2 would disappear into a
	// green run. `go` is guaranteed present -- it's the toolchain running
	// this very test -- so this child can't vanish that way.
	src := filepath.Join(t.TempDir(), "sleeper.go")
	program := "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Minute) }\n"
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", src)
	cmd.Dir = wt
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start a go run child to test against: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Snapshot forks ps and, per candidate, lsof -- poll rather than assume
	// the very first call lands after the child is scheduled and its cwd is
	// resolvable. `go run` compiles before its child appears, and both ps
	// and the per-pid lsof probes stretch under load, so the deadline here
	// is deliberately generous: the common case finishes in under a second,
	// and a wide ceiling costs nothing there while removing a whole class of
	// false failure on a loaded machine.
	deadline := time.Now().Add(30 * time.Second)
	var b []string
	for {
		b = ephemeral.Blockers(context.Background(), cfg, wt)
		if blockersContain(b, "running process") || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !blockersContain(b, "running process") {
		t.Fatalf("a running process inside the worktree must produce a 'running process' blocker, got %v", b)
	}
}

// TestBlockersRefusesWhenProcessEnumerationFails is the deterministic,
// portable regression test for Critical 1: discover.Snapshot used to
// swallow a failed `ps` as an empty result with no error, which reads
// identically to "nothing running" and would have silently authorised
// deletion out from under a live process. A pre-cancelled context forces
// the `ps` invocation inside discover.SnapshotErr to fail every time,
// without depending on being able to make the real `ps` binary fail.
func TestBlockersRefusesWhenProcessEnumerationFails(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/broken-ps")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The cancelled context also fails every git command inspect runs, so
	// other unrelated blockers fire too; assert the SPECIFIC
	// process-enumeration blocker.
	b := ephemeral.Blockers(ctx, cfg, wt)
	if !blockersContain(b, "could not enumerate processes") {
		t.Fatalf("a failed process probe must produce a 'could not enumerate processes' blocker, got %v", b)
	}
}

// TestBlockersRefusesWorktreeWithLiveSession is the regression test for the
// clause that matters most: discover.Snapshot deliberately EXCLUDES any
// process IsClaudeSession recognises from the process list it returns (see
// the "continue" right after "live[c.id] = p.PID" in
// internal/discover/procs.go) so it can surface in the sessions pane
// instead of the processes pane. That means
// TestBlockersRefusesRunningProcess cannot catch a live Claude session --
// this live-session clause is the ONLY guard against reaping a worktree out
// from under one, which is the single scenario this whole feature most
// needs to avoid.
//
// Two earlier attempts to skip this test were both wrong, on review.
// Round 1 claimed model.ClaudeProjectsDir() has no test override; Round 2
// disproved that (it reads $HOME, same as WT_CACHE_DIR moves
// discover.CacheRoot) but then claimed marking a session live requires
// "pretending to be Claude". It doesn't: discover.IsClaudeSession keys on
// nothing but a `--session-id <uuid>` substring in a REAL process's command
// line. So the double is exactly the TestBlockersRefusesRunningProcess `go
// run` sleeper, given that one extra flag -- no binary named claude, no
// impersonation, just the literal signal the production code is defined to
// detect.
func TestBlockersRefusesWorktreeWithLiveSession(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/live-session")
	startLiveSession(t, wt, wt, wt)

	waitForBlocker(t, func() []string {
		return ephemeral.Blockers(context.Background(), cfg, wt)
	}, "has a live Claude session")
}

// startLiveSession stands up the live-session fixture and returns the session
// id it used.
//
// sessionCwd is the directory the session process runs in; transcriptFor is
// the path its transcript directory is keyed on (Claude Code names that
// directory after whatever cwd the session was launched from). The two are
// separate parameters because they come apart in practice -- a session
// started in a subdirectory of a worktree keys its transcript on the
// subdirectory -- and that divergence is the whole subject of
// TestBlockersRefusesLiveSessionLaunchedFromASubdirectory.
//
// $HOME is redirected so model.ClaudeProjectsDir() reads the fixture rather
// than the developer's real transcripts.
func startLiveSession(t *testing.T, wt, sessionCwd, transcriptFor string) string {
	t.Helper()

	// go run's build cache defaults to a location under $HOME. Capture the
	// real one before HOME is overridden below and pin GOCACHE to it, or
	// every run of this test would rebuild the Go standard library into a
	// throwaway cache from scratch, risking the poll deadline below for a
	// reason that has nothing to do with the guard under test.
	goCacheOut, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatalf("could not determine GOCACHE: %v", err)
	}
	goCache := strings.TrimSpace(string(goCacheOut))

	home := t.TempDir()
	t.Setenv("HOME", home) // before anything below can read $HOME
	t.Setenv("GOCACHE", goCache)

	// discover.SessionsFor is queried by inspect() with the RAW worktree
	// path first (see reap.go), so the fixture is keyed off that same raw
	// path, not its resolved form.
	sessionID := "11111111-1111-1111-1111-111111111111"
	dir := filepath.Join(home, ".claude", "projects", model.ProjectDirName(transcriptFor))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Record shape copied from internal/discover/session_test.go's fixture,
	// not invented here.
	transcript := `{"type":"user","cwd":"` + sessionCwd + `","gitBranch":"feat/x","timestamp":"2026-08-13T00:00:00.000Z","message":{"content":[{"text":"hello"}]}}` + "\n" +
		`{"type":"ai-title","aiTitle":"A live session fixture","sessionId":"` + sessionID + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	// The double: a real `go run` child whose command line carries
	// --session-id <sessionID>, the same UUID the fixture above used --
	// discover.IsClaudeSession's only signal, matched honestly. Its cwd is
	// t.TempDir()'s /var/folders/..., never /tmp or /private/tmp, so it
	// survives the filter in discover.Snapshot that would otherwise drop it
	// silently for a reason unrelated to the guard under test.
	src := filepath.Join(t.TempDir(), "sleeper.go")
	program := "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Minute) }\n"
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionCwd, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", src, "--session-id", sessionID)
	cmd.Dir = sessionCwd
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start a go run child to test against: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return sessionID
}

// waitForBlocker polls until want appears, and fails if it never does.
//
// Snapshot forks ps and, per candidate, lsof -- poll rather than assume the
// very first call lands after the child is compiled, scheduled, and its cwd is
// resolvable. These are the guards standing between an unattended reap and a
// live Claude session, so the deadline is deliberately generous rather than
// tight: the common case finishes in under a second, and a wide ceiling costs
// nothing there while removing a whole class of false failure on a loaded
// machine -- a flaky red on exactly these guards would teach the wrong reflex
// ("that one's just noisy").
func waitForBlocker(t *testing.T, get func() []string, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var b []string
	for {
		b = get()
		if blockersContain(b, want) || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !blockersContain(b, want) {
		t.Fatalf("want a %q blocker, got %v", want, b)
	}
}

// TestBlockersRefusesLiveSessionLaunchedFromASubdirectory covers the hole the
// transcript-only probe left. inspect used to detect a live session ONLY by
// flattening the worktree root into ~/.claude/projects/<name> and reading that
// directory, and discover.SessionsFor returns nil on any ReadDir error. A
// session launched from a subdirectory -- which the spec explicitly permits,
// since a SessionEnd's cwd may be nested -- keys its transcript on the
// SUBDIRECTORY's flattened name, so the root query finds nothing and the
// worktree read as unoccupied. Same result for a session that has not flushed
// its first message yet, and for an unreadable ~/.claude/projects: a failed or
// empty probe authorising a deletion, the pattern already closed for git
// status and for ps.
//
// The fixture puts BOTH the session process and its transcript under
// <wt>/sub, and asserts the root-keyed transcript query comes back empty
// first -- so the only thing left that can produce the blocker is the session
// process's resolved cwd.
func TestBlockersRefusesLiveSessionLaunchedFromASubdirectory(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/subdir-session")
	sub := filepath.Join(wt, "sub")
	startLiveSession(t, wt, sub, sub)

	// Precondition: without this, the test could pass through the old
	// transcript path and prove nothing about the new clause. (An empty
	// directory is invisible to git status, so `sub` adds no dirty blocker.)
	if s := discover.SessionsFor(wt, nil); len(s) != 0 {
		t.Fatalf("precondition: the worktree root must have no transcript directory, got %d sessions", len(s))
	}

	waitForBlocker(t, func() []string {
		return ephemeral.Blockers(context.Background(), cfg, wt)
	}, "has a live Claude session")
}

// TestBlockersExemptsOnlyTheNamedEndingSession covers the exemption a
// SessionEnd hook depends on. That hook runs while its own session is still
// alive -- Claude forks `wt reap` and waits for it -- so ps reports the ending
// session and its transcript exists by definition. Without an exemption every
// hook-driven reap refuses itself and the feature never fires at all.
//
// The exemption must be exactly one id wide, so both directions are asserted:
// excluding the live session clears the blocker, and excluding some OTHER id
// leaves it standing. The second case is what proves a second terminal
// working in the same worktree still blocks.
func TestBlockersExemptsOnlyTheNamedEndingSession(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/ending-session")
	sessionID := startLiveSession(t, wt, wt, wt)

	// Establish the session is actually detected first; otherwise "no
	// live-session blocker" below would be satisfied by a fixture that never
	// started, and the test would be green for the wrong reason.
	waitForBlocker(t, func() []string {
		return ephemeral.Blockers(context.Background(), cfg, wt)
	}, "has a live Claude session")

	if b := ephemeral.BlockersExcluding(context.Background(), cfg, wt, sessionID); len(b) != 0 {
		t.Errorf("excluding the ending session must clear every blocker, got %v", b)
	}

	other := "22222222-2222-2222-2222-222222222222"
	b := ephemeral.BlockersExcluding(context.Background(), cfg, wt, other)
	if !blockersContain(b, "has a live Claude session") {
		t.Errorf("excluding a different session must leave the live one blocking, got %v", b)
	}
}

// TestReapExemptsTheEndingSession pins the exemption at the layer that
// actually deletes. Blockers is not the only gate: trash.SoftDelete re-checks
// model.PruneBlockers against the Workspace inspect built, and that check
// calls HasLiveSession on ws.Sessions -- so an exemption applied only to the
// blocker list, and not to the Sessions slice itself, would still refuse here,
// one layer down, with a different message.
func TestReapExemptsTheEndingSession(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/reap-ending")
	sessionID := startLiveSession(t, wt, wt, wt)

	waitForBlocker(t, func() []string {
		return ephemeral.Blockers(context.Background(), cfg, wt)
	}, "has a live Claude session")

	if err := ephemeral.Reap(context.Background(), cfg, wt, sessionID); err != nil {
		t.Fatalf("a reap naming its own ending session must proceed: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("the worktree directory must be gone")
	}
}

// TestBlockersRefusesWhenCheckedOutBranchDisagreesWithMarker is the
// regression test for Important 5: the marker's Branch is written once, at
// creation, and never updated. If the branch checked out in the worktree
// changes afterward (a `git switch` or `checkout -b` run inside it), the
// live checkout and the marker disagree, and a Workspace built from the
// stale marker value would carry the WRONG branch into trash.SoftDelete's
// entry. trash.Restore rebuilds with `git worktree add <path> <branch>` from
// exactly that recorded branch, so a stale value would restore onto the
// wrong branch, or fail outright -- the unrestorable-entry problem dropping
// ephemeral_delete_branch was meant to close, reopened from a different
// angle.
func TestBlockersRefusesWhenCheckedOutBranchDisagreesWithMarker(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/switched")
	r.Git(t, wt, "checkout", "-qb", "feat/switched-away")
	b := ephemeral.Blockers(context.Background(), cfg, wt)
	if !blockersContain(b, "does not match the marker") {
		t.Fatalf("a worktree whose checked-out branch disagrees with its marker must produce a 'does not match the marker' blocker, got %v", b)
	}
}

func TestReapRemovesOnlyWhenUnblocked(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/reapable")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("the worktree directory must be gone")
	}
	list := r.Git(t, r.Primary, "worktree", "list", "--porcelain")
	if strings.Contains(list, wt) {
		t.Error("git must no longer list the worktree")
	}
	// The branch must survive: trash.Restore rebuilds with
	// `git worktree add <path> <branch>` and no -b, so a deleted branch
	// would leave an unrestorable entry.
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/reapable"); err != nil {
		t.Error("reap must never delete the branch ref")
	}
}

func TestReapRecordsARestorableTrashEntry(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/restorable")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	entries := trash.Load()
	if len(entries) != 1 {
		t.Fatalf("trash holds %d entries, want 1 — an unattended reap must be recoverable", len(entries))
	}
	e := entries[0]
	if e.Path != wt || e.Branch != "feat/restorable" || e.Primary != r.Primary {
		t.Errorf("entry = %+v, want path/branch/primary matching the reaped worktree", e)
	}
	// The recorded entry must actually restore.
	if err := trash.Restore(context.Background(), e, config.Repo{}); err != nil {
		t.Fatalf("the entry reap recorded must be restorable: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Error("restore must recreate the worktree:", err)
	}
}

// A restored ephemeral has no marker (it went with the directory), so it is
// permanent from then on.
func TestRestoredEphemeralIsNoLongerReapable(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/promoted")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	if err := trash.Restore(context.Background(), trash.Load()[0], config.Repo{}); err != nil {
		t.Fatal(err)
	}
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) == 0 {
		t.Error("a restored worktree carries no marker and must never be reaped again")
	}
}

// delete_mode governs the dashboard's manual, confirmed delete. Nothing
// watches a reap, so it must stay recoverable regardless.
func TestReapSoftDeletesEvenWhenDeleteModeIsHard(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/hardmode")
	cfg.DeleteMode = "hard"
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	if len(trash.Load()) != 1 {
		t.Error("reap must soft-delete even under delete_mode = hard")
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/hardmode"); err != nil {
		t.Error("reap must not delete the branch even under delete_mode = hard")
	}
}

func TestReapRefusesWhenBlocked(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/blocked")
	r.WriteCommit(t, wt, "ahead.txt", "not pushed")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err == nil {
		t.Fatal("Reap must return an error when blocked")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Error("a blocked worktree must survive:", err)
	}
}

package discover

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/theczechr/wt/internal/model"
)

func TestParseStatusCountsChangedFiles(t *testing.T) {
	in := " M engine/jobs/foo.ts\n?? scratch.md\nA  src/new.ts\n"
	if got := ParseStatus(in); got != 3 {
		t.Errorf("ParseStatus = %d, want 3", got)
	}
	if got := ParseStatus(""); got != 0 {
		t.Errorf("ParseStatus(empty) = %d, want 0", got)
	}
	if got := ParseStatus("\n\n"); got != 0 {
		t.Errorf("ParseStatus(blank lines) = %d, want 0", got)
	}
}

func TestParseAheadBehind(t *testing.T) {
	// git prints "<behind>\t<ahead>"
	a, b := ParseAheadBehind("3\t7\n")
	if a != 7 || b != 3 {
		t.Errorf("got ahead=%d behind=%d, want 7/3", a, b)
	}
	a, b = ParseAheadBehind("")
	if a != 0 || b != 0 {
		t.Errorf("empty must yield 0/0, got %d/%d", a, b)
	}
}

// realFixture is `git status --porcelain=v2 --branch` output captured on
// this machine: a worktree with an upstream, clean ahead/behind, and two
// untracked paths.
const realFixture = `# branch.oid a1b2c3d4e5f60718293a4b5c6d7e8f901234567a
# branch.head feature/refund-payment-queue
# branch.upstream origin/feature/refund-payment-queue
# branch.ab +0 -0
? .claude/skills/verify/
? .worktrees/
`

func TestParseStatusV2RealFixture(t *testing.T) {
	dirty, ahead, behind, hasUpstream := ParseStatusV2(realFixture)
	if dirty != 2 {
		t.Errorf("dirty = %d, want 2", dirty)
	}
	if ahead != 0 || behind != 0 {
		t.Errorf("ahead=%d behind=%d, want 0/0", ahead, behind)
	}
	if !hasUpstream {
		t.Error("hasUpstream = false, want true: this fixture has a # branch.ab line")
	}
}

// TestParseStatusV2NoUpstreamHasNoBranchAbLine is the regression guard for
// the actual bug this change exists to fix: without a `# branch.ab` line,
// ahead/behind alone read exactly like "0 ahead, 0 behind" -- indistinguishable
// from a branch that's fully in sync with its upstream. hasUpstream is the
// only thing that tells the two apart, and it must come back false here.
func TestParseStatusV2NoUpstreamHasNoBranchAbLine(t *testing.T) {
	// Captured on this machine for a branch with no upstream: no
	// branch.upstream or branch.ab line at all.
	in := "# branch.oid bd026aa3c9694f8c861a4f7cdc4141c7cc588635\n" +
		"# branch.head feat/wt-implementation\n" +
		"1 M. N... 100644 100644 100644 abc def PERF.md\n"
	dirty, ahead, behind, hasUpstream := ParseStatusV2(in)
	if dirty != 1 {
		t.Errorf("dirty = %d, want 1", dirty)
	}
	if ahead != 0 || behind != 0 {
		t.Errorf("no upstream must yield ahead=0 behind=0, got %d/%d", ahead, behind)
	}
	if hasUpstream {
		t.Error("hasUpstream = true, want false: no # branch.ab line was present")
	}
}

func TestParseStatusV2AheadBehind(t *testing.T) {
	in := "# branch.oid abc\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +3 -7\n"
	dirty, ahead, behind, hasUpstream := ParseStatusV2(in)
	if dirty != 0 {
		t.Errorf("dirty = %d, want 0", dirty)
	}
	if ahead != 3 || behind != 7 {
		t.Errorf("ahead=%d behind=%d, want 3/7", ahead, behind)
	}
	if !hasUpstream {
		t.Error("hasUpstream = false, want true")
	}
}

func TestParseStatusV2Empty(t *testing.T) {
	dirty, ahead, behind, hasUpstream := ParseStatusV2("")
	if dirty != 0 || ahead != 0 || behind != 0 {
		t.Errorf("empty input must yield all zero, got dirty=%d ahead=%d behind=%d", dirty, ahead, behind)
	}
	if hasUpstream {
		t.Error("hasUpstream = true, want false: empty input has no branch.ab line")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wt-test", "GIT_AUTHOR_EMAIL=wt-test@example.com",
		"GIT_COMMITTER_NAME=wt-test", "GIT_COMMITTER_EMAIL=wt-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestFillStatusFallsBackToRevListWhenStatusFails reproduces a real defect
// found by measuring this optimisation against the user's actual repos: one
// worktree there fails `git status` outright, because of a submodule
// misconfiguration, while `git rev-list --left-right
// --count @{u}...HEAD` against the very same worktree still succeeds. The
// old two-call FillStatus reported Ahead/Behind correctly there because the
// two commands were independent; naively folding them into one
// `status --porcelain=v2 --branch` call would silently zero out Ahead/Behind
// too whenever status fails for a reason that has nothing to do with them.
//
// This reproduces an equivalent failure deterministically: a corrupted
// .git/index makes `git status` fail ("index file smaller than expected")
// while `git rev-list`, which never touches the index, keeps working.
func TestFillStatusFallsBackToRevListWhenStatusFails(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(dir+"/f", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f")
	runGit(t, dir, "commit", "-q", "-m", "init")
	runGit(t, dir, "remote", "add", "origin", "/nonexistent")
	runGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, dir, "branch", "--set-upstream-to=origin/main")
	if err := os.WriteFile(dir+"/f", []byte("xy"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-aq", "-m", "second") // now 1 ahead of the fake upstream

	// Sanity check the fixture behaves as expected before corrupting
	// anything: status succeeds and FillStatus reports ahead=1 normally.
	var sane model.Workspace
	sane.Path = dir
	FillStatus(context.Background(), &sane)
	if sane.Ahead != 1 || sane.Behind != 0 {
		t.Fatalf("fixture sanity check failed: got ahead=%d behind=%d, want 1/0", sane.Ahead, sane.Behind)
	}
	if !sane.StatusKnown {
		t.Fatalf("fixture sanity check failed: StatusKnown must be true when `git status` succeeds")
	}
	if !sane.HasUpstream {
		t.Fatalf("fixture sanity check failed: HasUpstream must be true, an upstream was configured")
	}

	// Corrupt the index so `git status` fails; rev-list doesn't read it.
	if err := os.Remove(dir + "/.git/index"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/.git/index", []byte("garbage not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "status", "--porcelain=v2", "--branch").Run(); err == nil {
		t.Fatal("fixture sanity check failed: git status must fail against the corrupted index")
	}

	var w model.Workspace
	w.Path = dir
	FillStatus(context.Background(), &w)
	if w.Ahead != 1 || w.Behind != 0 {
		t.Errorf("FillStatus must fall back to rev-list and still report ahead=1 behind=0 when status fails, got ahead=%d behind=%d", w.Ahead, w.Behind)
	}
	if w.DirtyCount != 0 {
		t.Errorf("DirtyCount = %d, want 0 (status itself failed, so it truly is unknown)", w.DirtyCount)
	}
	if w.StatusKnown {
		t.Error("StatusKnown must be false: the rev-list fallback recovers Ahead/Behind but never git status itself, so DirtyCount is genuinely unknown, not clean")
	}
	if !w.HasUpstream {
		t.Error("HasUpstream must still be true: @{u} resolved via the rev-list fallback even though combined status failed for an unrelated reason")
	}
}

// TestFillStatusNoUpstreamReportsHasUpstreamFalse is the real-git companion
// to TestParseStatusV2NoUpstreamHasNoBranchAbLine: a freshly-created branch
// with no upstream configured at all must come back with HasUpstream false
// end-to-end through FillStatus, and LocalOnlyCommitCount must correctly
// count every commit on it as local-only (this repo has no remote, so the
// entire history qualifies).
func TestFillStatusNoUpstreamReportsHasUpstreamFalse(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	for _, content := range []string{"one", "two", "three"} {
		if err := os.WriteFile(dir+"/f", []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "f")
		runGit(t, dir, "commit", "-q", "-m", content)
	}

	var w model.Workspace
	w.Path = dir
	FillStatus(context.Background(), &w)
	if w.HasUpstream {
		t.Error("HasUpstream = true, want false: no upstream was ever configured for this branch")
	}
	if !w.StatusKnown {
		t.Error("StatusKnown = false, want true: `git status` succeeds fine on a plain, uncorrupted repo")
	}

	n, ok := LocalOnlyCommitCount(context.Background(), dir)
	if !ok {
		t.Fatal("LocalOnlyCommitCount reported !ok against a plain, healthy repo")
	}
	if n != 3 {
		t.Errorf("LocalOnlyCommitCount = %d, want 3 (this repo has no remote, so all 3 commits are local-only)", n)
	}
}

// TestLocalOnlyCommitCountExcludesRemoteHistory is the case
// LocalOnlyCommitCount's doc comment is about: a branch built on top of
// shared history that IS on a remote must only count the commits unique to
// it, not the whole ancestry.
func TestLocalOnlyCommitCountExcludesRemoteHistory(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(dir+"/f", []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f")
	runGit(t, dir, "commit", "-q", "-m", "base")
	runGit(t, dir, "remote", "add", "origin", "/nonexistent")
	runGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")

	// Two more commits on top, never pushed -- refs/remotes/origin/main
	// still points at the base commit.
	for _, content := range []string{"two", "three"} {
		if err := os.WriteFile(dir+"/f", []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "commit", "-aq", "-m", content)
	}

	n, ok := LocalOnlyCommitCount(context.Background(), dir)
	if !ok {
		t.Fatal("LocalOnlyCommitCount reported !ok")
	}
	if n != 2 {
		t.Errorf("LocalOnlyCommitCount = %d, want 2 (the base commit is on origin/main, only the 2 on top are local-only)", n)
	}
}

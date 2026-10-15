package ephemeral_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
	"github.com/theczechr/ephemera/internal/model"
)

func head(t *testing.T, r *gittest.Repo, dir string) string {
	t.Helper()
	return strings.TrimSpace(r.Git(t, dir, "rev-parse", "HEAD"))
}

func TestOwnCommitsCountsOnlyYourCommitsSinceStart(t *testing.T) {
	r := gittest.New(t)
	_, wt := newEphemeral(t, r, "feat/own")
	m, _ := ephemeral.ReadMarker(wt)
	m.Start = head(t, r, wt)
	r.WriteCommit(t, wt, "mine.txt", "x")
	// Someone else's commit, e.g. pulled from the PR author.
	r.Git(t, wt, "-c", "user.email=other@test.invalid", "commit", "-q", "--allow-empty", "-m", "theirs")
	n, err := ephemeral.OwnCommits(context.Background(), wt, m)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("OwnCommits = %d, want 1 (only the commit authored by user.email)", n)
	}
}

func TestOwnCommitsV1FallbackCountsUnpushedOwnWork(t *testing.T) {
	r := gittest.New(t)
	_, wt := newEphemeral(t, r, "feat/v1")
	m, _ := ephemeral.ReadMarker(wt) // v1-style: Start empty
	if n, _ := ephemeral.OwnCommits(context.Background(), wt, m); n != 0 {
		t.Fatalf("pushed branch: OwnCommits = %d, want 0", n)
	}
	r.WriteCommit(t, wt, "local.txt", "x")
	if n, _ := ephemeral.OwnCommits(context.Background(), wt, m); n != 1 {
		t.Fatalf("after a local commit: OwnCommits = %d, want 1", n)
	}
}

func TestOwnCommitsWithoutUserEmailCountsEveryCommit(t *testing.T) {
	r := gittest.New(t)
	_, wt := newEphemeral(t, r, "feat/noemail")
	m, _ := ephemeral.ReadMarker(wt)
	m.Start = head(t, r, wt)
	r.Git(t, wt, "-c", "user.email=other@test.invalid", "commit", "-q", "--allow-empty", "-m", "theirs")
	r.Git(t, r.Primary, "config", "--unset", "user.email")
	// OwnCommits runs plain git, which would otherwise fall back to the
	// developer's ~/.gitconfig user.email and filter the commit out.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	n, err := ephemeral.OwnCommits(context.Background(), wt, m)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("OwnCommits = %d, want 1: with no user.email every commit must count as yours (keep longer, never reap early)", n)
	}
}

func TestOwnCommitsErrorsOnUnreadableHistory(t *testing.T) {
	r := gittest.New(t)
	_, wt := newEphemeral(t, r, "feat/badstart")
	m, _ := ephemeral.ReadMarker(wt)
	m.Start = "0000000000000000000000000000000000000000"
	if _, err := ephemeral.OwnCommits(context.Background(), wt, m); err == nil {
		t.Fatal("an unknown start commit must be an error, never 0")
	}
}

func TestLastUsedNeverEarlierThanCreatedAt(t *testing.T) {
	r := gittest.New(t)
	_, wt := newEphemeral(t, r, "feat/lastused")
	m, _ := ephemeral.ReadMarker(wt)
	future := time.Now().Add(48 * time.Hour)
	m.CreatedAt = future
	if got := ephemeral.LastUsed(context.Background(), wt, m, nil); got.Before(future) {
		t.Fatalf("LastUsed = %v, must be at least created_at %v", got, future)
	}
}

func TestLastUsedTakesTheLatestSignal(t *testing.T) {
	r := gittest.New(t)
	_, wt := newEphemeral(t, r, "feat/signals")
	m, _ := ephemeral.ReadMarker(wt)
	m.CreatedAt = time.Now().Add(-10 * 24 * time.Hour)
	later := time.Now().Add(time.Hour)
	sessions := []model.Session{{ID: "s", Mtime: later}}
	if got := ephemeral.LastUsed(context.Background(), wt, m, sessions); !got.Equal(later) {
		t.Errorf("LastUsed = %v, want the session mtime %v", got, later)
	}
	m.LastOpenedAt = later.Add(time.Hour)
	if got := ephemeral.LastUsed(context.Background(), wt, m, sessions); !got.Equal(m.LastOpenedAt) {
		t.Errorf("LastUsed = %v, want last_opened_at %v", got, m.LastOpenedAt)
	}
	// The git index counts as use: touch it into the future.
	idx := filepath.Join(r.Primary, ".git", "worktrees", filepath.Base(wt), "index")
	idxTime := later.Add(5 * time.Hour)
	if err := os.Chtimes(idx, idxTime, idxTime); err != nil {
		t.Fatal(err)
	}
	// Compared within a second: filesystems differ in mtime precision.
	if got := ephemeral.LastUsed(context.Background(), wt, m, sessions); got.Sub(idxTime).Abs() > time.Second {
		t.Errorf("LastUsed = %v, want the index mtime %v", got, idxTime)
	}
}

func TestPRStateUnknownWhenLookupFails(t *testing.T) {
	t.Cleanup(ephemeral.SetPRStateLookupForTest(func(context.Context, string, ephemeral.Marker) (string, error) {
		return "", errors.New("gh: not logged in")
	}))
	if got := ephemeral.PRState(context.Background(), "/p", ephemeral.Marker{Branch: "feat/x"}); got != "" {
		t.Fatalf("PRState = %q, want \"\" (unknown) when gh fails", got)
	}
}

func TestPRStatePassesThroughKnownState(t *testing.T) {
	t.Cleanup(ephemeral.SetPRStateLookupForTest(func(context.Context, string, ephemeral.Marker) (string, error) {
		return "MERGED", nil
	}))
	if got := ephemeral.PRState(context.Background(), "/p", ephemeral.Marker{Branch: "feat/x"}); got != "MERGED" {
		t.Fatalf("PRState = %q, want MERGED", got)
	}
}

// Email case is not identity: a commit made as WT@Test.Invalid is still the
// user's own when user.email is wt@test.invalid. Missing it would read the
// worktree as "no commits of your own" and reap it on sight.
func TestOwnCommitsMatchesAuthorEmailCaseInsensitively(t *testing.T) {
	r := gittest.New(t)
	_, wt := newEphemeral(t, r, "feat/email-case")
	m, _ := ephemeral.ReadMarker(wt)
	m.Start = head(t, r, wt)
	r.Git(t, wt, "-c", "user.email=WT@Test.Invalid", "commit", "-q", "--allow-empty", "-m", "mine, other case")
	n, err := ephemeral.OwnCommits(context.Background(), wt, m)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("OwnCommits = %d, want 1", n)
	}
}

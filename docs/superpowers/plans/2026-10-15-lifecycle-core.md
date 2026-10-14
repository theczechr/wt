# Lifecycle Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make ephemeral worktrees reliably clean themselves up: a precise "done" rule, triggers that run without the dashboard, removal that works with submodules, and `wt <PR>`.

**Architecture:** The reap predicate in `internal/ephemeral` keeps its fail-closed safety checks and gains a pure `Decide` function that judges gathered `Facts`. `trash.SoftDeleteWith` adds the two new removal behaviours (`--force` for submodules, `-D` for wt-created branches). New CLI surfaces (`wt reap --all`, `wt hooks`, `wt timer`, `wt branches`, `wt <PR>`) live in `cmd/wt` as testable `run*` functions that take writers and return exit codes.

**Tech Stack:** Go 1.26.5, stdlib only (plus the existing `BurntSushi/toml` and bubbletea deps), git ≥ 2.38, `gh` CLI (optional at runtime).

**Spec:** `docs/superpowers/specs/2026-10-12-lifecycle-core-design.md`

## Global Constraints

- Prerequisite: PR #1 (`feat/t3-code-coexistence`) and PR #2 (`chore/rename-to-ephemera`) are merged. Module path is `github.com/theczechr/ephemera`; the command stays `wt`.
- Branch for this work: `feat/lifecycle-core`, cut from `main` after #1 and #2.
- No new third-party dependencies.
- All git behaviour is tested against real repositories through `internal/gittest`; nothing mocks git. `gh` is faked only by a script placed first on `PATH`, or by the package-level lookup hook added in Task 4.
- Every destructive decision fails closed: a fact that cannot be read is a blocker, never "assume fine".
- `idle_days` default `3`; `reap_trigger_interval` default `"10m"` (spec §6).
- Removal passes exactly one `--force`, and only for worktrees with initialized submodules (spec §4.1).
- A branch is deleted only when `branch_created_by_wt` is true **and** `git rev-list <branch> --not --remotes` is empty, using `git branch -D` (spec §4.2).
- A bare argument matching `^#?[0-9]+$` is always a PR number, never a branch (spec §5.1). `wt open <branch>` remains the escape hatch for a branch with a numeric name.
- Run `gofmt -l . && go vet ./... && go test ./...` before every commit; all three must be clean.
- Commit messages follow the repo's conventional style (`feat:`, `fix:`, `test:`, `docs:`).

## Plan-level decisions (not in the spec; confirm before executing)

1. **Sweep grace of 15 minutes.** The spec's "no own commits → done as soon as safe" would let the background sweep that `wt <branch>` itself triggers (spec §3.2) remove the worktree it just created, before the user's session starts. Sweeps therefore skip worktrees created or opened less than `DefaultSweepGrace = 15m` ago. The `SessionEnd` hook and a hand-typed `wt reap <path>` apply no grace, so spec §8's first criterion still holds.
2. **Fork PR heads live at `refs/remotes/wt-pr/<N>`**, and `pr/<N>` is branched from that ref. Fetching `refs/pull/N/head` straight into `pr/<N>` (spec §5.2) would leave its commits outside every `refs/remotes/*` ref, so §4.2's `--not --remotes` check could never pass and `pr/<N>` branches would pile up. `wt-pr` is not a configured remote, so `git fetch --prune` never deletes it (verified 2026-10-15).
3. **`branch_created_by_wt` is also set for remote-only `wt <branch>` checkouts.** wt creates the local branch there too, and it is fully on the remote, so it gets the same §4.2 cleanup.
4. **PR state for a PR-marked worktree comes from `gh pr view <N> --json state`.** `gh pr list` is keyed by `headRefName`, which collides across forks (many forks push from `main`). The call happens only when the worktree has own commits, so it stays rare.
5. **"Listing them on demand" (spec §2.5) is `wt branches --unpushed`.** The dashboard footer shows the count and names that command.

## Review Focus

1. **A sweep reaps a worktree the user just created, before first use.** A user runs `wt feat/x`; the background sweep that same command spawned must not remove `.worktrees/x` while their shell is still cd'ing into it. Pinned in Task 5 (`TestSweepGraceProtectsAFreshWorktree`).
2. **A worktree with own commits and no session transcript.** `LastUsed` must never fall back to the zero time, or `idle` becomes infinite and committed-to worktrees are reaped instantly. It must floor at `created_at`. Pinned in Task 4 (`TestLastUsedNeverEarlierThanCreatedAt`).
3. **`user.email` unset in the repo.** Own commits must then count every commit since `start` (keep the worktree longer), never zero (reap instantly). Pinned in Task 4 (`TestOwnCommitsWithoutUserEmailCountsEveryCommit`).
4. **`gh` missing, logged out, or rate-limited during a sweep.** PR state must read as unknown (falling through to the idle rule), never as an error blocker and never as MERGED. Pinned in Task 4 (`TestPRStateUnknownWhenLookupFails`).
5. **A branch literally named `1234`.** `wt 1234` now means PR 1234; `wt open 1234` must still open the branch. Pinned in Task 14 (`TestOpenStillOpensANumericBranch`).

---

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/config/config.go` | `idle_days`, `reap_trigger_interval` keys and their `Effective*` accessors | 1 |
| `internal/ephemeral/marker.go` | Marker v2 fields, `UpdateMarker`, `TouchOpened` | 2 |
| `internal/ephemeral/done.go` | Pure done rule: `Facts`, `Verdict`, `Decide` | 3 |
| `internal/ephemeral/facts.go` | Gathering facts from git/gh/fs: `OwnCommits`, `LastUsed`, `PRState`, lock/submodule/unpushed probes | 4 |
| `internal/ephemeral/export_test.go` | Test hook for the PR-state lookup | 4 |
| `internal/ephemeral/reap.go` | Safety predicate + done rule wired together; `Inspect`, `ReapWith`, `Decision` | 5, 7 |
| `internal/gittest/gittest.go` | `AddSubmodule` fixture helper | 6 |
| `internal/model/paths.go` | `PRRemoteRef(n)` shared ref-name helper | 6 |
| `internal/trash/delete.go`, `trash.go` | `RemoveOptions`, `SoftDeleteWith`, entry `PR`/`BranchDeleted`, restore re-fetch | 6 |
| `internal/ephemeral/state.go` | `StateDir`, lock file, 4-column `LogReap` | 8 |
| `internal/ephemeral/sweep.go` | Locked sweep returning `[]Decision`, dry-run | 8 |
| `cmd/wt/reap.go` | `wt reap [<path> \| --all] [--dry-run] [--json] [--quiet]` | 9 |
| `internal/ephemeral/trigger.go`, `cmd/wt/autosweep.go` | Rate-limited background sweep after any command | 10 |
| `cmd/wt/main.go` | Record `start`, touch `last_opened_at`, dispatch new commands | 11, 14, 16 |
| `cmd/wt/hooks.go` | `wt hooks install claude [--yes]` | 12 |
| `cmd/wt/timer.go` | `wt timer install\|uninstall\|status` | 13 |
| `cmd/wt/pr.go` | `wt <PR>` parse, repo resolution, gh, open/update | 14 |
| `internal/discover/orphans.go`, `cmd/wt/branches.go`, `internal/ui/ui.go` | Unpushed-branches count and listing | 15 |
| `README.md` | User-facing docs for all of the above | 16 |

---

### Task 1: Config keys `idle_days` and `reap_trigger_interval`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/lifecycle_test.go` (create)

**Interfaces:**
- Produces: `Repo.IdleDays int`, `Config.IdleDays int`, `Config.ReapTriggerInterval string`, `func (c Config) EffectiveIdleDays(repo string) int`, `func (c Config) EffectiveReapTriggerInterval() time.Duration`

- [ ] **Step 1: Write the failing test**

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/config"
)

func load(t *testing.T, body string) config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEffectiveIdleDaysDefaultsToThree(t *testing.T) {
	cfg := load(t, "[repo.server]\n")
	if got := cfg.EffectiveIdleDays("server"); got != 3 {
		t.Fatalf("EffectiveIdleDays = %d, want 3", got)
	}
}

func TestEffectiveIdleDaysPrefersRepoOverGlobal(t *testing.T) {
	cfg := load(t, "idle_days = 7\n[repo.server]\nidle_days = 2\n[repo.web]\n")
	if got := cfg.EffectiveIdleDays("server"); got != 2 {
		t.Errorf("server = %d, want the repo's own 2", got)
	}
	if got := cfg.EffectiveIdleDays("web"); got != 7 {
		t.Errorf("web = %d, want the global 7", got)
	}
}

func TestEffectiveIdleDaysIgnoresNonPositive(t *testing.T) {
	cfg := load(t, "idle_days = 0\n[repo.server]\nidle_days = -1\n")
	if got := cfg.EffectiveIdleDays("server"); got != 3 {
		t.Fatalf("EffectiveIdleDays = %d, want the default 3 for non-positive values", got)
	}
}

func TestEffectiveReapTriggerInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"":                                  10 * time.Minute,
		"reap_trigger_interval = \"30m\"\n": 30 * time.Minute,
		"reap_trigger_interval = \"soon\"\n": 10 * time.Minute,
		"reap_trigger_interval = \"-5m\"\n": 10 * time.Minute,
	}
	for body, want := range cases {
		if got := load(t, body).EffectiveReapTriggerInterval(); got != want {
			t.Errorf("%q: got %s, want %s", body, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'Idle|Trigger' -v`
Expected: FAIL — `cfg.EffectiveIdleDays undefined`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add to `Repo` (after `Ephemeral`):

```go
	// IdleDays overrides Config.IdleDays for this repo. See
	// Config.EffectiveIdleDays.
	IdleDays int `toml:"idle_days"`
```

Add to `Config` (after `HerdrStartupDashboard`):

```go
	// IdleDays is how long an ephemeral worktree with commits of your own
	// may sit unused before the reaper treats it as done. See
	// EffectiveIdleDays.
	IdleDays int `toml:"idle_days"`

	// ReapTriggerInterval rate-limits the background sweep that any wt
	// command may start. A Go duration string. See
	// EffectiveReapTriggerInterval.
	ReapTriggerInterval string `toml:"reap_trigger_interval"`
```

Add below `EffectiveTrashRetention`:

```go
const (
	defaultIdleDays            = 3
	defaultReapTriggerInterval = 10 * time.Minute
)

// EffectiveIdleDays returns the idle limit for repo: the repo's own
// idle_days, else the global one, else 3. Non-positive values count as
// absent -- a zero limit would make every committed-to worktree "idle" the
// moment it was created.
func (c Config) EffectiveIdleDays(repo string) int {
	if r, ok := c.Repos[repo]; ok && r.IdleDays > 0 {
		return r.IdleDays
	}
	if c.IdleDays > 0 {
		return c.IdleDays
	}
	return defaultIdleDays
}

// EffectiveReapTriggerInterval returns the minimum gap between background
// sweeps. An unparseable or non-positive value falls back to 10m rather
// than failing the config load: the keys next to it govern deletion, and a
// typo here must not stop wt from reading them.
func (c Config) EffectiveReapTriggerInterval() time.Duration {
	d, err := time.ParseDuration(c.ReapTriggerInterval)
	if err != nil || d <= 0 {
		return defaultReapTriggerInterval
	}
	return d
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (all, including pre-existing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/lifecycle_test.go
git commit -m "feat(config): add idle_days and reap_trigger_interval"
```

---

### Task 2: Marker v2

**Files:**
- Modify: `internal/ephemeral/marker.go`
- Modify: `internal/ephemeral/marker_test.go`

**Interfaces:**
- Produces: `Marker.Start string`, `Marker.PR int`, `Marker.BranchCreatedByWT bool`, `Marker.LastOpenedAt time.Time`; `func UpdateMarker(worktree string, edit func(*Marker)) error`; `func TouchOpened(worktree string, now time.Time) error`. `WriteMarker` stamps version 2; `ReadMarker` accepts 1 and 2.

- [ ] **Step 1: Write the failing tests**

In `internal/ephemeral/marker_test.go`, change `TestWriteMarkerStampsVersion`'s assertion to version 2:

```go
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
```

Append:

```go
func TestReadMarkerAcceptsVersionOne(t *testing.T) {
	dir := t.TempDir()
	body := `{"version":1,"repo":"server","branch":"feat/x","primary":"/p","created_at":"2026-08-13T10:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, ephemeral.MarkerName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatalf("a v1 marker must stay readable: %v", err)
	}
	if m.Start != "" {
		t.Errorf("Start = %q, want empty for v1 (selects the fallback own-commit rule)", m.Start)
	}
}

func TestMarkerV2RoundTripsNewFields(t *testing.T) {
	dir := t.TempDir()
	opened := time.Now().UTC().Truncate(time.Second)
	in := ephemeral.Marker{
		Repo: "server", Branch: "pr/7", Primary: "/p", CreatedAt: opened,
		Start: "abc123", PR: 7, BranchCreatedByWT: true, LastOpenedAt: opened,
	}
	if err := ephemeral.WriteMarker(dir, in); err != nil {
		t.Fatal(err)
	}
	got, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Start != "abc123" || got.PR != 7 || !got.BranchCreatedByWT || !got.LastOpenedAt.Equal(opened) {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

func TestTouchOpenedUpgradesV1KeepingStartEmpty(t *testing.T) {
	dir := t.TempDir()
	body := `{"version":1,"repo":"server","branch":"feat/x","primary":"/p","created_at":"2026-08-13T10:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, ephemeral.MarkerName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := ephemeral.TouchOpened(dir, now); err != nil {
		t.Fatal(err)
	}
	got, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || !got.LastOpenedAt.Equal(now) || got.Start != "" {
		t.Errorf("got %+v, want v2 with LastOpenedAt set and Start still empty", got)
	}
}

func TestTouchOpenedOnUnmarkedDirIsAnError(t *testing.T) {
	if err := ephemeral.TouchOpened(t.TempDir(), time.Now()); err == nil {
		t.Error("touching a directory without a marker must fail, never create one")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ephemeral/ -run 'Marker|TouchOpened' -v`
Expected: FAIL — `unknown field Start`, `undefined: ephemeral.TouchOpened`.

- [ ] **Step 3: Implement**

In `internal/ephemeral/marker.go`, set `const markerVersion = 2` and replace the `Marker` struct:

```go
// Marker records which branch of which repo an ephemeral worktree holds.
//
// Version 2 adds the fields the done rule needs (spec §2.4). A version 1
// marker stays valid: Start is then empty, which selects the fallback
// own-commit rule in OwnCommits.
type Marker struct {
	Version   int       `json:"version"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Primary   string    `json:"primary"`
	CreatedAt time.Time `json:"created_at"`

	// Start is the commit HEAD pointed at when wt created the worktree.
	Start string `json:"start,omitempty"`
	// PR is the pull request this worktree was opened for, if any.
	PR int `json:"pr,omitempty"`
	// BranchCreatedByWT is true when wt created the local branch (a fork
	// PR's pr/<N>, or a local copy of a remote-only branch).
	BranchCreatedByWT bool `json:"branch_created_by_wt,omitempty"`
	// LastOpenedAt is the last time wt handed this worktree to the user.
	LastOpenedAt time.Time `json:"last_opened_at,omitzero"`
}
```

In `ReadMarker`, replace the version check:

```go
	if m.Version != 1 && m.Version != markerVersion {
		return m, fmt.Errorf("%s: unsupported version %d", MarkerName, m.Version)
	}
```

Append:

```go
// UpdateMarker reads the marker, applies edit, and writes it back as the
// current version. It never creates a marker: an unreadable one is an error,
// because a marker is an input to a destructive decision.
func UpdateMarker(worktree string, edit func(*Marker)) error {
	m, err := ReadMarker(worktree)
	if err != nil {
		return err
	}
	edit(&m)
	return WriteMarker(worktree, m)
}

// TouchOpened records that wt just handed this worktree to the user. It
// feeds LastUsed and the sweep grace period.
func TouchOpened(worktree string, now time.Time) error {
	return UpdateMarker(worktree, func(m *Marker) { m.LastOpenedAt = now })
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ephemeral/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ephemeral/marker.go internal/ephemeral/marker_test.go
git commit -m "feat(ephemeral): marker v2 with start, pr and last_opened_at"
```

---

### Task 3: The pure done rule

**Files:**
- Create: `internal/ephemeral/done.go`
- Test: `internal/ephemeral/done_test.go`

**Interfaces:**
- Produces:

```go
type Facts struct {
	OwnCommits int
	PRState    string        // "OPEN", "MERGED", "CLOSED", or "" when unknown or no PR
	LastUsed   time.Time
	OpenedAt   time.Time     // later of created_at and last_opened_at
	Grace      time.Duration // 0 disables the grace check
	IdleDays   int
	Now        time.Time
}
type Verdict struct {
	Done   bool
	Reason string
}
func Decide(f Facts) Verdict
```

- [ ] **Step 1: Write the failing test**

```go
package ephemeral_test

import (
	"strings"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	base := ephemeral.Facts{IdleDays: 3, Now: now, LastUsed: now.Add(-time.Hour), OpenedAt: now.Add(-2 * time.Hour)}

	cases := []struct {
		name     string
		edit     func(*ephemeral.Facts)
		done     bool
		contains string
	}{
		{"review: no own commits", func(f *ephemeral.Facts) {}, true, "no commits of your own"},
		{"own commits, PR open, fresh", func(f *ephemeral.Facts) { f.OwnCommits = 2; f.PRState = "OPEN" }, false, "PR open"},
		{"own commits, no PR, fresh", func(f *ephemeral.Facts) { f.OwnCommits = 1 }, false, "no PR"},
		{"own commits, PR merged", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.PRState = "MERGED" }, true, "PR merged"},
		{"own commits, PR closed", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.PRState = "CLOSED" }, true, "PR closed"},
		{"own commits, idle past limit", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.LastUsed = now.Add(-4 * day) }, true, "idle"},
		{"own commits, idle exactly at limit", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.LastUsed = now.Add(-3 * day) }, true, "idle"},
		{"own commits, idle just under limit", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.LastUsed = now.Add(-3*day + time.Minute) }, false, "idle"},
		{"unknown PR state falls through to idle", func(f *ephemeral.Facts) { f.OwnCommits = 1; f.PRState = ""; f.LastUsed = now.Add(-5 * day) }, true, "idle"},
		{"grace beats everything", func(f *ephemeral.Facts) { f.Grace = 15 * time.Minute; f.OpenedAt = now.Add(-time.Minute) }, false, "grace"},
		{"grace expired", func(f *ephemeral.Facts) { f.Grace = 15 * time.Minute; f.OpenedAt = now.Add(-16 * time.Minute) }, true, "no commits of your own"},
		{"zero grace ignores a fresh worktree", func(f *ephemeral.Facts) { f.OpenedAt = now }, true, "no commits of your own"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := base
			c.edit(&f)
			v := ephemeral.Decide(f)
			if v.Done != c.done {
				t.Errorf("Done = %v, want %v (reason %q)", v.Done, c.done, v.Reason)
			}
			if !strings.Contains(v.Reason, c.contains) {
				t.Errorf("Reason = %q, want it to mention %q", v.Reason, c.contains)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ephemeral/ -run TestDecide -v`
Expected: FAIL — `undefined: ephemeral.Facts`.

- [ ] **Step 3: Implement**

```go
package ephemeral

import (
	"fmt"
	"time"
)

// Facts are the observations the done rule judges (spec §2.2). Gathering
// them touches git, gh and the filesystem (facts.go); judging them touches
// nothing, so every combination is table-testable.
type Facts struct {
	OwnCommits int
	// PRState is "OPEN", "MERGED", "CLOSED", or "" when there is no PR or
	// its state could not be read. Unknown is never treated as merged.
	PRState  string
	LastUsed time.Time
	// OpenedAt is the later of the marker's created_at and last_opened_at.
	OpenedAt time.Time
	// Grace protects a worktree opened less than this long ago. Sweeps set
	// it; the SessionEnd hook and `wt reap <path>` leave it 0.
	Grace    time.Duration
	IdleDays int
	Now      time.Time
}

// Verdict is the done rule's answer and the reason a person would read in
// reap.log or `wt reap --dry-run`.
type Verdict struct {
	Done   bool
	Reason string
}

// Decide applies spec §2.2. It is only consulted after every safety check
// has passed; it decides "is this finished", never "is this safe".
func Decide(f Facts) Verdict {
	if f.Grace > 0 {
		if age := f.Now.Sub(f.OpenedAt); age < f.Grace {
			return Verdict{Reason: fmt.Sprintf("opened %s ago (sweep grace %s)", age.Round(time.Second), f.Grace)}
		}
	}
	if f.OwnCommits == 0 {
		return Verdict{Done: true, Reason: "no commits of your own"}
	}
	switch f.PRState {
	case "MERGED":
		return Verdict{Done: true, Reason: "PR merged"}
	case "CLOSED":
		return Verdict{Done: true, Reason: "PR closed"}
	}
	idle := f.Now.Sub(f.LastUsed)
	if idle >= time.Duration(f.IdleDays)*24*time.Hour {
		return Verdict{Done: true, Reason: fmt.Sprintf("idle %s (limit %dd)", days(idle), f.IdleDays)}
	}
	pr := "no PR"
	if f.PRState == "OPEN" {
		pr = "PR open"
	}
	return Verdict{Reason: fmt.Sprintf("%d commit(s) of your own; %s; idle %s of %dd",
		f.OwnCommits, pr, days(idle), f.IdleDays)}
}

func days(d time.Duration) string { return fmt.Sprintf("%.1fd", d.Hours()/24) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ephemeral/ -run TestDecide -v`
Expected: PASS (12 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/ephemeral/done.go internal/ephemeral/done_test.go
git commit -m "feat(ephemeral): pure done rule"
```

---

### Task 4: Gathering facts

**Files:**
- Create: `internal/ephemeral/facts.go`
- Create: `internal/ephemeral/export_test.go`
- Test: `internal/ephemeral/facts_test.go`

**Interfaces:**
- Consumes: `Marker` (Task 2).
- Produces:

```go
func OwnCommits(ctx context.Context, worktree string, m Marker) (int, error)
func LastUsed(ctx context.Context, worktree string, m Marker, sessions []model.Session) time.Time
func PRState(ctx context.Context, primary string, m Marker) string
// unexported, used by Task 5/7:
func gitOut(ctx context.Context, dir string, args ...string) (string, error)
func isLocked(ctx context.Context, primary, worktree string) (bool, error)
func hasSubmodules(ctx context.Context, worktree string) (bool, error)
func submodulesDirty(ctx context.Context, worktree string) (bool, error)
func unpushedCount(ctx context.Context, primary, branch string) (int, error)
func openedAt(m Marker) time.Time
// test hook (export_test.go):
func SetPRStateLookupForTest(f func(ctx context.Context, primary string, m Marker) (string, error)) (restore func())
```

- [ ] **Step 1: Write the failing tests**

`internal/ephemeral/export_test.go`:

```go
package ephemeral

import "context"

// SetPRStateLookupForTest swaps the gh-backed PR lookup for f and returns a
// function restoring the original. Tests must never reach the real gh.
func SetPRStateLookupForTest(f func(ctx context.Context, primary string, m Marker) (string, error)) func() {
	old := prStateLookup
	prStateLookup = f
	return func() { prStateLookup = old }
}
```

`internal/ephemeral/facts_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ephemeral/ -run 'OwnCommits|LastUsed|PRState' -v`
Expected: FAIL — `undefined: ephemeral.OwnCommits`, `undefined: prStateLookup`.

- [ ] **Step 3: Implement**

`internal/ephemeral/facts.go`:

```go
package ephemeral

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/model"
)

// prCacheTTL matches aggregate's PR cache lifetime, so a sweep right after
// a dashboard refresh costs no gh call.
const prCacheTTL = 10 * time.Minute

// gitOut runs a read-only git command in dir and returns trimmed stdout.
// --no-optional-locks matters beyond politeness: without it `git status`
// rewrites the index, and the index mtime is one of LastUsed's signals.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"--no-optional-locks", "-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	if err != nil {
		return "", withStderr(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// OwnCommits counts the commits on the worktree's HEAD that the repo's own
// user.email authored since wt created the worktree (spec §2.2).
//
// With a v2 marker the range is start..HEAD. A v1 marker has no start, so
// the range becomes "on HEAD but on no remote". An unset user.email drops
// the author filter, so every commit counts as yours: that keeps a worktree
// longer, which is the safe direction.
func OwnCommits(ctx context.Context, worktree string, m Marker) (int, error) {
	args := []string{"rev-list", "--count"}
	if email, err := gitOut(ctx, worktree, "config", "user.email"); err == nil && email != "" {
		// --fixed-strings: emails contain '.' and '+', which are regex
		// metacharacters. Verified against git 2.50 on 2026-10-15.
		args = append(args, "--fixed-strings", "--author=<"+email+">")
	}
	if m.Start != "" {
		args = append(args, m.Start+"..HEAD")
	} else {
		args = append(args, "HEAD", "--not", "--remotes")
	}
	out, err := gitOut(ctx, worktree, args...)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, fmt.Errorf("unreadable commit count %q: %w", out, err)
	}
	return n, nil
}

// openedAt is the later of when wt created the worktree and when it last
// handed it to the user.
func openedAt(m Marker) time.Time {
	if m.LastOpenedAt.After(m.CreatedAt) {
		return m.LastOpenedAt
	}
	return m.CreatedAt
}

// LastUsed is the latest sign of use (spec §2.3). It is floored at
// openedAt, so a worktree with no transcript and an untouched index reads
// as "used when created", never as the zero time -- which would make idle
// infinite and reap a committed-to worktree on sight.
func LastUsed(ctx context.Context, worktree string, m Marker, sessions []model.Session) time.Time {
	t := openedAt(m)
	for _, s := range sessions {
		if s.Mtime.After(t) {
			t = s.Mtime
		}
	}
	for _, name := range []string{"index", "logs/HEAD"} {
		p, err := gitOut(ctx, worktree, "rev-parse", "--git-path", name)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(worktree, p)
		}
		if st, err := os.Stat(p); err == nil && st.ModTime().After(t) {
			t = st.ModTime()
		}
	}
	return t
}

// prStateLookup is swapped out in tests (export_test.go).
var prStateLookup = lookupPRState

// PRState returns the state of the worktree's PR, or "" when there is none
// or it cannot be read. Failure is deliberately silent: an unknown state
// falls through to the idle rule, and a missing or logged-out gh must
// neither block every reap nor ever read as MERGED.
func PRState(ctx context.Context, primary string, m Marker) string {
	s, err := prStateLookup(ctx, primary, m)
	if err != nil {
		return ""
	}
	return s
}

func lookupPRState(ctx context.Context, primary string, m Marker) (string, error) {
	if m.PR != 0 {
		// gh pr list is keyed by headRefName, which collides across forks,
		// so a known PR number is asked about directly.
		cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(m.PR), "--json", "state")
		cmd.Dir = primary
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		var v struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return "", err
		}
		return v.State, nil
	}
	prs, ok := discover.CachedPRs(primary, prCacheTTL)
	if !ok {
		fresh, err := discover.PRsForRepo(ctx, primary)
		if err != nil {
			return "", err
		}
		_ = discover.WritePRCache(primary, fresh)
		prs = fresh
	}
	return prs[m.Branch].State, nil
}

// isLocked reports whether git considers worktree locked. `git worktree
// list --porcelain` prints a bare "locked" line (or "locked <reason>") in
// the worktree's block; verified against git 2.50 on 2026-10-15.
func isLocked(ctx context.Context, primary, worktree string) (bool, error) {
	out, err := gitOut(ctx, primary, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, block := range strings.Split(out, "\n\n") {
		var path string
		locked := false
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				path = strings.TrimPrefix(line, "worktree ")
			case line == "locked" || strings.HasPrefix(line, "locked "):
				locked = true
			}
		}
		if path != "" && sameDir(path, worktree) {
			return locked, nil
		}
	}
	return false, nil
}

// hasSubmodules reports whether any submodule is initialized in worktree.
// `git submodule status` prefixes an uninitialized one with '-'.
func hasSubmodules(ctx context.Context, worktree string) (bool, error) {
	out, err := gitOut(ctx, worktree, "submodule", "status", "--recursive")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, "-") {
			return true, nil
		}
	}
	return false, nil
}

// submodulesDirty runs the same status check inside every initialized
// submodule, recursively (spec §2.1). The superproject's own status already
// reports " M sub" for untracked content in a submodule (verified), so this
// is a second, independent layer -- the one --force removal leans on.
func submodulesDirty(ctx context.Context, worktree string) (bool, error) {
	out, err := gitOut(ctx, worktree, "submodule", "foreach", "--quiet", "--recursive",
		"git status --porcelain --ignore-submodules=none")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// unpushedCount counts commits on branch that exist on no remote-tracking
// ref. Fork PR heads are fetched to refs/remotes/wt-pr/<N>, so they count
// as "on a remote" here too.
func unpushedCount(ctx context.Context, primary, branch string) (int, error) {
	out, err := gitOut(ctx, primary, "rev-list", "--count", "refs/heads/"+branch, "--not", "--remotes")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ephemeral/ -run 'OwnCommits|LastUsed|PRState' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ephemeral/facts.go internal/ephemeral/facts_test.go internal/ephemeral/export_test.go
git commit -m "feat(ephemeral): gather own commits, last use and PR state"
```

---

### Task 5: Wire the done rule into the reap predicate

**Files:**
- Modify: `internal/ephemeral/reap.go`
- Modify: `internal/ephemeral/reap_test.go`
- Modify: `internal/ephemeral/sweep.go` (signature-only change to keep compiling; Task 8 rewrites it)
- Modify: `internal/ephemeral/sweep_test.go`
- Modify: `cmd/wt/main.go` (dashboard refresh call site)

**Interfaces:**
- Consumes: `Decide`, `Facts` (Task 3); `OwnCommits`, `LastUsed`, `PRState`, `isLocked`, `submodulesDirty`, `hasSubmodules`, `openedAt` (Task 4); `cfg.EffectiveIdleDays` (Task 1).
- Produces:

```go
type InspectOptions struct {
	ExcludeSessionID string
	Grace            time.Duration
	Now              time.Time // zero means time.Now()
}
type Decision struct {
	Path     string   `json:"path"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Reap     bool     `json:"reap"`
	Reason   string   `json:"reason"`
	Blockers []string `json:"blockers,omitempty"`
}
const DefaultSweepGrace = 15 * time.Minute
func Inspect(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) Decision
func ReapWith(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) (Decision, error)
// unchanged signatures: Blockers, BlockersExcluding, Reap
type SweepOptions struct {
	DryRun bool
	Grace  time.Duration
}
```

- [ ] **Step 1: Write the failing tests**

In `reap_test.go`, make `newEphemeral` stub the PR lookup. Insert right after `t.Setenv("WT_CACHE_DIR", t.TempDir())`:

```go
	t.Cleanup(ephemeral.SetPRStateLookupForTest(func(context.Context, string, ephemeral.Marker) (string, error) {
		return "", nil
	}))
```

Replace `TestBlockersRefusesBranchWithNoUpstream` and `TestBlockersRefusesUnpushedCommits` with:

```go
// Regression fixture for the 277 "branch has no upstream" refusals in
// reap.log: a review worktree with no upstream and no commits of yours is
// done. The branch outlives the worktree, so nothing is lost.
func TestBlockersReapsNoUpstreamWithoutOwnCommits(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/no-upstream")
	r.Git(t, wt, "branch", "--unset-upstream")
	if b := ephemeral.Blockers(context.Background(), cfg, wt); len(b) != 0 {
		t.Fatalf("want no blockers for a no-upstream review worktree, got %v", b)
	}
}

func TestBlockersKeepsOwnUnpushedWorkUntilDone(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/ahead")
	r.WriteCommit(t, wt, "ahead.txt", "not pushed")
	b := ephemeral.Blockers(context.Background(), cfg, wt)
	if !blockersContain(b, "not done yet") {
		t.Fatalf("own commits, no PR, fresh: want a 'not done yet' blocker, got %v", b)
	}
}

func TestInspectReapsOwnWorkOncePRMerged(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/merged")
	r.WriteCommit(t, wt, "ahead.txt", "x")
	t.Cleanup(ephemeral.SetPRStateLookupForTest(func(context.Context, string, ephemeral.Marker) (string, error) {
		return "MERGED", nil
	}))
	d := ephemeral.Inspect(context.Background(), cfg, wt, ephemeral.InspectOptions{})
	if !d.Reap || d.Reason != "PR merged" {
		t.Fatalf("got %+v, want reap with reason 'PR merged'", d)
	}
}

func TestInspectReapsOwnWorkAfterIdleDays(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/idle")
	r.WriteCommit(t, wt, "ahead.txt", "x")
	d := ephemeral.Inspect(context.Background(), cfg, wt, ephemeral.InspectOptions{
		Now: time.Now().Add(4 * 24 * time.Hour),
	})
	if !d.Reap || !strings.Contains(d.Reason, "idle") {
		t.Fatalf("got %+v, want reap for being idle past 3 days", d)
	}
}

func TestBlockersRefusesLockedWorktree(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/locked")
	r.Git(t, r.Primary, "worktree", "lock", wt)
	if b := ephemeral.Blockers(context.Background(), cfg, wt); !blockersContain(b, "locked") {
		t.Fatalf("a locked worktree must be refused, got %v", b)
	}
}

func TestSweepGraceProtectsAFreshWorktree(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/fresh")
	d := ephemeral.Inspect(context.Background(), cfg, wt, ephemeral.InspectOptions{Grace: ephemeral.DefaultSweepGrace})
	if d.Reap || !strings.Contains(d.Reason, "grace") {
		t.Fatalf("a worktree created moments ago must survive a sweep, got %+v", d)
	}
}

func TestInspectReportsBlockersAsReason(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/dirty-reason")
	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := ephemeral.Inspect(context.Background(), cfg, wt, ephemeral.InspectOptions{})
	if d.Reap || !strings.Contains(d.Reason, "uncommitted") || d.Repo != "demo" || d.Branch != "feat/dirty-reason" {
		t.Fatalf("got %+v", d)
	}
}
```

`TestReapRefusesWhenBlocked` keeps working unchanged (own commit → not done).

In `sweep_test.go`, change each `ephemeral.Sweep(..., map[string]string{"demo": r.Primary})` call to:

```go
	decisions, err := ephemeral.Sweep(context.Background(), cfg, map[string]string{"demo": r.Primary}, ephemeral.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
```

and in `TestSweepRemovesOnlyTheReapableOne` replace the `len(removed) != 1` check with:

```go
	reaped := 0
	for _, d := range decisions {
		if d.Reap {
			reaped++
		}
	}
	if reaped != 1 {
		t.Fatalf("decisions %+v, want exactly the clean worktree reaped", decisions)
	}
```

(In the other two sweep tests, assert `len(decisions) == 0` where they asserted `len(removed) == 0`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ephemeral/ -v 2>&1 | tail -30`
Expected: FAIL to compile — `undefined: ephemeral.Inspect`, `ephemeral.InspectOptions`, `SweepOptions`.

- [ ] **Step 3: Implement**

In `reap.go`:

1. Add imports `"time"`.
2. Extend `state`:

```go
type state struct {
	marker   Marker
	ws       model.Workspace
	blockers []string
	verdict  Verdict
	// hasSubmodules selects `git worktree remove --force` (spec §4.1).
	hasSubmodules bool
	// deleteBranch is true when the branch is wt's own and fully on a
	// remote (spec §4.2). Set by Task 7.
	deleteBranch bool
}
```

3. Add the option/decision types and wrappers above `Blockers`:

```go
// DefaultSweepGrace protects a worktree created or opened this recently
// from sweeps. Without it, the sweep `wt <branch>` itself triggers could
// remove the worktree it just made. The SessionEnd hook and `wt reap
// <path>` apply none.
const DefaultSweepGrace = 15 * time.Minute

// InspectOptions tunes one inspection.
type InspectOptions struct {
	// ExcludeSessionID is the one session treated as already over. See Reap.
	ExcludeSessionID string
	Grace            time.Duration
	// Now is "now" for the idle and grace checks; zero means time.Now().
	Now time.Time
}

// Decision is one worktree's verdict, as printed by `wt reap --dry-run`
// and logged to reap.log.
type Decision struct {
	Path     string   `json:"path"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Reap     bool     `json:"reap"`
	Reason   string   `json:"reason"`
	Blockers []string `json:"blockers,omitempty"`
}

func (st state) decision(path string) Decision {
	d := Decision{
		Path: path, Repo: st.marker.Repo, Branch: st.marker.Branch,
		Reap: len(st.blockers) == 0, Blockers: st.blockers,
	}
	if d.Reap {
		d.Reason = st.verdict.Reason
	} else {
		d.Reason = strings.Join(st.blockers, "; ")
	}
	return d
}

// Inspect judges worktree without touching it.
func Inspect(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) Decision {
	return inspect(ctx, cfg, worktree, opts).decision(worktree)
}
```

4. Change `BlockersExcluding` to `return inspect(ctx, cfg, worktree, InspectOptions{ExcludeSessionID: excludeSessionID}).blockers`, and the `inspect` signature to `func inspect(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) state`. Inside `inspect`, replace both uses of `excludeSessionID` with `opts.ExcludeSessionID`.

5. After the `registeredWorktree` check, add:

```go
	switch locked, err := isLocked(ctx, m.Primary, worktree); {
	case err != nil:
		b = append(b, fmt.Sprintf("could not read the worktree's lock state: %v", err))
	case locked:
		b = append(b, "worktree is locked (git worktree lock)")
	}
```

6. After the `dirtyCount` block, add:

```go
	switch dirty, err := submodulesDirty(ctx, worktree); {
	case err != nil:
		b = append(b, fmt.Sprintf("could not read submodule status: %v", err))
	case dirty:
		b = append(b, "a submodule has uncommitted or untracked changes")
	}
	hasSubs, err := hasSubmodules(ctx, worktree)
	if err != nil {
		b = append(b, fmt.Sprintf("could not read submodule state: %v", err))
	}
```

7. Replace the whole `hasUpstream, ahead, err := upstreamState(...)` block (the assignment plus its `switch`) with:

```go
	// No longer a blocker (spec §2.1): the branch outlives the worktree, so
	// neither an upstream nor an ahead count protects committed work. Still
	// recorded, because the dashboard's delete confirmation reads them.
	hasUpstream, ahead, _ := upstreamState(ctx, worktree)
	ws.HasUpstream = hasUpstream
	ws.Ahead = ahead
```

8. In `dirtyCount`, change the arguments to `"status", "--porcelain", "--ignore-submodules=none"`.

9. Replace the final `return state{marker: m, ws: ws, blockers: b}` with:

```go
	st := state{marker: m, ws: ws, blockers: b, hasSubmodules: hasSubs}
	if len(b) > 0 {
		return st
	}
	// Only a worktree that is SAFE to remove is asked whether it is DONE.
	own, err := OwnCommits(ctx, worktree, m)
	if err != nil {
		st.blockers = append(st.blockers, fmt.Sprintf("could not count your commits: %v", err))
		return st
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	f := Facts{
		OwnCommits: own,
		LastUsed:   LastUsed(ctx, worktree, m, ws.Sessions),
		OpenedAt:   openedAt(m),
		Grace:      opts.Grace,
		IdleDays:   cfg.EffectiveIdleDays(m.Repo),
		Now:        now,
	}
	if own > 0 {
		f.PRState = PRState(ctx, m.Primary, m)
	}
	st.verdict = Decide(f)
	if !st.verdict.Done {
		st.blockers = append(st.blockers, "not done yet: "+st.verdict.Reason)
	}
	return st
```

10. Replace `Reap` with `ReapWith` plus a wrapper (keep `Reap`'s existing doc comment on the wrapper):

```go
// ReapWith removes worktree if and only if inspection finds no blockers. The
// inspection runs here, immediately before removal, never earlier (spec
// §3.4): a session opened mid-sweep still blocks.
func ReapWith(ctx context.Context, cfg config.Config, worktree string, opts InspectOptions) (Decision, error) {
	st := inspect(ctx, cfg, worktree, opts)
	d := st.decision(worktree)
	if !d.Reap {
		return d, fmt.Errorf("refusing to remove %s: %s", worktree, strings.Join(st.blockers, "; "))
	}
	if _, err := trash.SoftDelete(ctx, st.ws, st.marker.Primary); err != nil {
		d.Reap, d.Reason = false, err.Error()
		return d, fmt.Errorf("soft-deleting %s: %w", worktree, err)
	}
	return d, nil
}

func Reap(ctx context.Context, cfg config.Config, worktree, excludeSessionID string) error {
	_, err := ReapWith(ctx, cfg, worktree, InspectOptions{ExcludeSessionID: excludeSessionID})
	return err
}
```

11. In `sweep.go`, change the signature and body minimally (Task 8 replaces it):

```go
// SweepOptions tunes a sweep.
type SweepOptions struct {
	DryRun bool
	Grace  time.Duration
}

func Sweep(ctx context.Context, cfg config.Config, primaries map[string]string, opts SweepOptions) ([]Decision, error) {
	var out []Decision
	for name, primary := range primaries {
		r, ok := cfg.Repos[name]
		if !ok || !r.EphemeralEnabled() {
			continue
		}
		root := filepath.Join(primary, r.EphemeralDirOrDefault())
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // no ephemeral dir yet is the normal case, not a failure
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name())
			if _, err := ReadMarker(path); err != nil {
				continue // not wt's to remove
			}
			io := InspectOptions{Grace: opts.Grace}
			if opts.DryRun {
				out = append(out, Inspect(ctx, cfg, path, io))
				continue
			}
			d, err := ReapWith(ctx, cfg, path, io)
			LogReap(path, err)
			out = append(out, d)
		}
	}
	return out, nil
}
```

(add `"time"` to sweep.go's imports.)

12. In `cmd/wt/main.go`'s dashboard `refresh` closure, change the sweep call to:

```go
			_, _ = ephemeral.Sweep(ctx, cfg, resolve.Primaries(cfg),
				ephemeral.SweepOptions{Grace: ephemeral.DefaultSweepGrace})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/ephemeral/ ./cmd/wt/ -v 2>&1 | tail -40`
Expected: PASS, including every pre-existing reap test.

- [ ] **Step 5: Commit**

```bash
git add internal/ephemeral cmd/wt/main.go
git commit -m "feat(ephemeral): judge done-ness after safety; drop the upstream gate"
```

---

### Task 6: Removal options in trash, submodule fixture, PR restore

**Files:**
- Modify: `internal/gittest/gittest.go`
- Modify: `internal/model/paths.go`
- Modify: `internal/trash/trash.go`, `internal/trash/delete.go`
- Test: `internal/trash/remove_options_test.go` (create)

**Interfaces:**
- Produces:

```go
// gittest
func (r *Repo) AddSubmodule(t *testing.T) // adds submodule "sub" to main, pushed to origin
// model
func PRRemoteRef(n int) string // "refs/remotes/wt-pr/<n>"
// trash
type RemoveOptions struct {
	Force        bool
	DeleteBranch bool
	PR           int
}
func SoftDeleteWith(ctx context.Context, ws model.Workspace, primary string, opts RemoveOptions) (Entry, error)
// Entry gains: PR int `json:"pr,omitempty"`, BranchDeleted bool `json:"branch_deleted,omitempty"`
```

- [ ] **Step 1: Write the failing tests**

`internal/trash/remove_options_test.go`:

```go
package trash_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/gittest"
	"github.com/theczechr/ephemera/internal/model"
	"github.com/theczechr/ephemera/internal/trash"
)

func cleanWS(path, branch string) model.Workspace {
	return model.Workspace{Repo: "demo", Path: path, Branch: branch, StatusKnown: true, SessionsKnown: true}
}

func TestSoftDeleteWithForceRemovesSubmoduleWorktree(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	r.AddSubmodule(t)
	wt := filepath.Join(r.Primary, ".worktrees", "sub-wt")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/sub", wt)
	r.Git(t, wt, "submodule", "update", "--init", "-q")

	if _, err := trash.SoftDelete(context.Background(), cleanWS(wt, "feat/sub"), r.Primary); err == nil {
		t.Fatal("precondition: plain removal must fail on a submodule worktree (git refuses)")
	}
	if _, err := trash.SoftDeleteWith(context.Background(), cleanWS(wt, "feat/sub"), r.Primary,
		trash.RemoveOptions{Force: true}); err != nil {
		t.Fatalf("SoftDeleteWith(Force): %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("the worktree must be gone")
	}
}

func TestSoftDeleteWithForceStillRefusesALockedWorktree(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	wt := filepath.Join(r.Primary, ".worktrees", "locked")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/locked", wt)
	r.Git(t, r.Primary, "worktree", "lock", wt)
	if _, err := trash.SoftDeleteWith(context.Background(), cleanWS(wt, "feat/locked"), r.Primary,
		trash.RemoveOptions{Force: true}); err == nil {
		t.Fatal("one --force must never override a lock")
	}
}

func TestSoftDeleteWithDeleteBranchRecordsIt(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	wt := filepath.Join(r.Primary, ".worktrees", "pr-9")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "pr/9", wt)
	e, err := trash.SoftDeleteWith(context.Background(), cleanWS(wt, "pr/9"), r.Primary,
		trash.RemoveOptions{DeleteBranch: true, PR: 9})
	if err != nil {
		t.Fatal(err)
	}
	if !e.BranchDeleted || e.PR != 9 {
		t.Errorf("entry = %+v, want BranchDeleted and PR 9", e)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/pr/9"); err == nil {
		t.Error("the branch must be deleted")
	}
}

func TestRestoreRefetchesADeletedPRBranch(t *testing.T) {
	t.Setenv("WT_CACHE_DIR", t.TempDir())
	r := gittest.New(t)
	// A PR head that exists only as refs/pull/9/head on origin.
	r.Git(t, r.Origin, "checkout", "-q", "-b", "tmp-fork")
	r.WriteCommit(t, r.Origin, "fork.txt", "x")
	sha := strings.TrimSpace(r.Git(t, r.Origin, "rev-parse", "HEAD"))
	r.Git(t, r.Origin, "update-ref", "refs/pull/9/head", sha)
	r.Git(t, r.Origin, "checkout", "-q", "main")
	r.Git(t, r.Origin, "branch", "-qD", "tmp-fork")

	wt := filepath.Join(r.Primary, ".worktrees", "pr-9")
	e := trash.Entry{Path: wt, Repo: "demo", Branch: "pr/9", Primary: r.Primary, PR: 9, BranchDeleted: true}
	if err := trash.Restore(context.Background(), e, config.Repo{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := strings.TrimSpace(r.Git(t, wt, "rev-parse", "HEAD")); got != sha {
		t.Errorf("restored HEAD = %s, want the PR head %s", got, sha)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/trash/ -run 'SoftDeleteWith|Refetches' -v`
Expected: FAIL — `r.AddSubmodule undefined`, `undefined: trash.SoftDeleteWith`.

- [ ] **Step 3: Implement**

`internal/gittest/gittest.go`, append:

```go
// AddSubmodule adds a submodule at "sub" (a separate throwaway repo) to the
// primary's main, commits it and pushes main to origin, so worktrees created
// afterwards contain it.
//
// git ≥ 2.38 refuses file:// submodule transport by default. The override
// is set with t.Setenv so it reaches wt's own git subprocesses too (Restore
// re-runs `git submodule update --init` through bootstrap).
func (r *Repo) AddSubmodule(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
	sub := filepath.Join(filepath.Dir(r.Origin), "subrepo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git(t, sub, "init", "-q", "-b", "main")
	r.configure(t, sub)
	r.WriteCommit(t, sub, "lib.txt", "lib\n")
	r.Git(t, r.Primary, "submodule", "add", "-q", sub, "sub")
	r.Git(t, r.Primary, "commit", "-qm", "add submodule")
	r.Git(t, r.Primary, "push", "-q", "origin", "main")
}
```

`internal/model/paths.go`, append (add `"fmt"` import):

```go
// PRRemoteRef is where wt keeps a fetched pull request head:
// refs/remotes/wt-pr/<n>. Under refs/remotes so `--not --remotes` counts the
// PR's commits as pushed; under a namespace that is not a configured remote
// so `git fetch --prune` never deletes it.
func PRRemoteRef(n int) string { return fmt.Sprintf("refs/remotes/wt-pr/%d", n) }
```

`internal/trash/trash.go`, extend `Entry`:

```go
	// PR is the pull request the worktree was opened for, if any.
	PR int `json:"pr,omitempty"`
	// BranchDeleted is true when the removal also deleted the local branch
	// (spec §4.2); Restore then fetches it again.
	BranchDeleted bool `json:"branch_deleted,omitempty"`
```

`internal/trash/delete.go`: replace `SoftDelete`'s body with a call, and add `RemoveOptions`, `SoftDeleteWith` and `recreateBranch`:

```go
// RemoveOptions extends SoftDelete for the unattended reaper.
type RemoveOptions struct {
	// Force passes exactly one --force to `git worktree remove`. git refuses
	// to remove a worktree with initialized submodules without it; callers
	// set it only after proving the worktree and its submodules clean. One
	// --force never overrides a lock (that takes two).
	Force bool
	// DeleteBranch deletes the local branch with `git branch -D` after the
	// worktree is gone. -D, not -d: -d refuses a branch unmerged into HEAD,
	// which a fork PR branch always is. The caller's proof that every commit
	// is on a remote is the safety gate.
	DeleteBranch bool
	// PR is recorded in the trash entry so Restore can fetch it again.
	PR int
}

func SoftDelete(ctx context.Context, ws model.Workspace, primary string) (Entry, error) {
	return SoftDeleteWith(ctx, ws, primary, RemoveOptions{})
}

// SoftDeleteWith is SoftDelete with RemoveOptions. PruneBlockers is still
// consulted first and still the only gate in this package.
func SoftDeleteWith(ctx context.Context, ws model.Workspace, primary string, opts RemoveOptions) (Entry, error) {
	if blockers := ws.PruneBlockers(); len(blockers) > 0 {
		return Entry{}, &BlockedError{Blockers: blockers}
	}
	args := []string{"-C", primary, "worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, ws.Path)
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return Entry{}, fmt.Errorf("git worktree remove: %w: %s", err, bytes.TrimSpace(out))
	}
	e := Entry{
		Path: ws.Path, Repo: ws.Repo, Branch: ws.Branch, Primary: primary,
		DeletedAt: time.Now(), PR: opts.PR,
	}
	if opts.DeleteBranch {
		// A failed delete leaves the branch: the safe outcome, so not an error.
		if exec.CommandContext(ctx, "git", "-C", primary, "branch", "-D", ws.Branch).Run() == nil {
			e.BranchDeleted = true
		}
	}
	if err := Add(e); err != nil {
		return e, fmt.Errorf("worktree removed but failed to record a trash entry: %w", err)
	}
	return e, nil
}

// recreateBranch fetches a branch that SoftDeleteWith deleted. A PR entry
// comes back from refs/pull/<n>/head (works for fork and same-repo PRs);
// anything else from the remote branch of the same name.
func recreateBranch(ctx context.Context, e Entry, remote string) error {
	from := remote + "/" + e.Branch
	fetch := []string{"-C", e.Primary, "fetch", "-q", remote, e.Branch}
	if e.PR > 0 {
		from = model.PRRemoteRef(e.PR)
		fetch = []string{"-C", e.Primary, "fetch", "-q", remote,
			fmt.Sprintf("+refs/pull/%d/head:%s", e.PR, from)}
	}
	if out, err := exec.CommandContext(ctx, "git", fetch...).CombinedOutput(); err != nil {
		return fmt.Errorf("fetching %s again: %w: %s", e.Branch, err, bytes.TrimSpace(out))
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", e.Primary, "branch", e.Branch, from).CombinedOutput(); err != nil {
		return fmt.Errorf("recreating branch %s: %w: %s", e.Branch, err, bytes.TrimSpace(out))
	}
	return nil
}
```

In `Restore`, after the occupied-path check and before `git worktree add`:

```go
	if e.BranchDeleted {
		if err := recreateBranch(ctx, e, repoCfg.RemoteOrDefault()); err != nil {
			return fmt.Errorf("refusing to restore: %w", err)
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/trash/ ./internal/gittest/ -v 2>&1 | tail -30`
Expected: PASS (new and existing).

- [ ] **Step 5: Commit**

```bash
git add internal/gittest internal/model/paths.go internal/trash
git commit -m "feat(trash): force-remove submodule worktrees, delete wt branches, refetch on restore"
```

---

### Task 7: Reap uses force and branch deletion

**Files:**
- Modify: `internal/ephemeral/reap.go`
- Test: `internal/ephemeral/reap_removal_test.go` (create)

**Interfaces:**
- Consumes: `trash.SoftDeleteWith`, `trash.RemoveOptions` (Task 6); `unpushedCount` (Task 4); `state.hasSubmodules`, `state.deleteBranch` (Task 5).

- [ ] **Step 1: Write the failing tests**

```go
package ephemeral_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
	"github.com/theczechr/ephemera/internal/trash"
)

// Regression fixture for "working trees containing submodules cannot be
// moved or removed": every clean server ephemeral failed this way.
func TestReapRemovesACleanSubmoduleWorktree(t *testing.T) {
	r := gittest.New(t)
	r.AddSubmodule(t)
	cfg, wt := newEphemeral(t, r, "feat/with-sub")
	r.Git(t, wt, "submodule", "update", "--init", "-q")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("the submodule worktree must be gone")
	}
}

func TestReapRefusesUntrackedFileInsideSubmodule(t *testing.T) {
	r := gittest.New(t)
	r.AddSubmodule(t)
	cfg, wt := newEphemeral(t, r, "feat/sub-dirty")
	r.Git(t, wt, "submodule", "update", "--init", "-q")
	if err := os.WriteFile(filepath.Join(wt, "sub", "scratch.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err == nil {
		t.Fatal("an untracked file inside a submodule must block removal")
	}
	if _, err := os.Stat(filepath.Join(wt, "sub", "scratch.txt")); err != nil {
		t.Error("the file must survive:", err)
	}
}

func markWTBranch(t *testing.T, wt string) {
	t.Helper()
	if err := ephemeral.UpdateMarker(wt, func(m *ephemeral.Marker) { m.BranchCreatedByWT = true }); err != nil {
		t.Fatal(err)
	}
}

func TestReapDeletesWTBranchFullyOnARemote(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/wt-made")
	markWTBranch(t, wt)
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/wt-made"); err == nil {
		t.Error("a wt-created branch fully on the remote must be deleted")
	}
	if e := trash.Load(); len(e) != 1 || !e[0].BranchDeleted {
		t.Errorf("trash = %+v, want one entry with BranchDeleted", e)
	}
}

func TestReapKeepsWTBranchWithUnpushedCommits(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/wt-made-ahead")
	markWTBranch(t, wt)
	// Someone else's unpushed commit: not "own", so the worktree is done,
	// but the branch holds work that exists nowhere else.
	r.Git(t, wt, "-c", "user.email=other@test.invalid", "commit", "-q", "--allow-empty", "-m", "theirs")
	d, err := ephemeral.ReapWith(context.Background(), cfg, wt, ephemeral.InspectOptions{Now: time.Now()})
	if err != nil {
		t.Fatalf("ReapWith: %v (decision %+v)", err, d)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/wt-made-ahead"); err != nil {
		t.Error("a branch with commits on no remote must be kept")
	}
}

func TestReapNeverDeletesABranchWTDidNotCreate(t *testing.T) {
	r := gittest.New(t)
	cfg, wt := newEphemeral(t, r, "feat/users-own")
	if err := ephemeral.Reap(context.Background(), cfg, wt, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TryGit(r.Primary, "show-ref", "--verify", "--quiet", "refs/heads/feat/users-own"); err != nil {
		t.Error("a branch wt did not create must survive the reap")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ephemeral/ -run 'Submodule|WTBranch|DidNotCreate' -v`
Expected: FAIL — submodule worktree refused by git; branch not deleted.

- [ ] **Step 3: Implement**

In `inspect`, after `st.verdict = Decide(f)` (in the done branch, i.e. replace the final `if !st.verdict.Done { ... } return st` with):

```go
	if !st.verdict.Done {
		st.blockers = append(st.blockers, "not done yet: "+st.verdict.Reason)
		return st
	}
	if m.BranchCreatedByWT {
		// Unreadable means "keep the branch": the safe outcome, not a blocker.
		if n, err := unpushedCount(ctx, m.Primary, m.Branch); err == nil && n == 0 {
			st.deleteBranch = true
		}
	}
	return st
```

In `ReapWith`, replace the `trash.SoftDelete(...)` call with:

```go
	ro := trash.RemoveOptions{Force: st.hasSubmodules, DeleteBranch: st.deleteBranch, PR: st.marker.PR}
	if _, err := trash.SoftDeleteWith(ctx, st.ws, st.marker.Primary, ro); err != nil {
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ephemeral/ -v 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ephemeral
git commit -m "feat(ephemeral): reap submodule worktrees and wt-created branches"
```

---

### Task 8: State dir, sweep lock, reason column

**Files:**
- Create: `internal/ephemeral/state.go`
- Modify: `internal/ephemeral/hook.go` (remove `LogReap`, now in state.go)
- Modify: `internal/ephemeral/sweep.go`
- Modify: `cmd/wt/main.go` (the `LogReap` call in `case "reap"`)
- Test: `internal/ephemeral/state_test.go` (create), `internal/ephemeral/sweep_test.go`

**Interfaces:**
- Produces:

```go
func StateDir() string                                  // $WT_STATE_DIR or ~/.local/state/wt
func LogReap(worktree string, err error, reason string) // 4 tab-separated columns
var ErrSweepBusy = errors.New("another sweep is already running")
func Sweep(ctx context.Context, cfg config.Config, primaries map[string]string, opts SweepOptions) ([]Decision, error)
```

- [ ] **Step 1: Write the failing tests**

`internal/ephemeral/state_test.go`:

```go
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
```

Append to `export_test.go`:

```go
// (add "testing" to this file's import block, next to "context")

// HoldSweepLockForTest takes the sweep lock as a separate open file, the
// way a second wt process would.
func HoldSweepLockForTest(t *testing.T) func() {
	release, ok, err := acquireLock(sweepLockName)
	if err != nil || !ok {
		t.Fatalf("could not take the sweep lock: ok=%v err=%v", ok, err)
	}
	return release
}
```

In `newEphemeral` (reap_test.go), add `t.Setenv("WT_STATE_DIR", t.TempDir())` next to the `WT_CACHE_DIR` line, so no test writes the developer's real reap.log.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ephemeral/ -run 'LogReap|Sweep' -v`
Expected: FAIL — `too many arguments in call to ephemeral.LogReap`, `undefined: ErrSweepBusy`.

- [ ] **Step 3: Implement**

Delete `LogReap` from `hook.go` (and now-unused imports). Create `internal/ephemeral/state.go`:

```go
package ephemeral

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// sweepLockName is the lock file every sweep takes (spec §3.4).
const sweepLockName = "sweep.lock"

// ErrSweepBusy means another sweep holds the lock. Not a failure: the
// other sweep is doing the same work.
var ErrSweepBusy = errors.New("another sweep is already running")

// StateDir is where wt keeps reap.log, the sweep lock and the trigger
// timestamp: $WT_STATE_DIR, else ~/.local/state/wt.
func StateDir() string {
	if v := os.Getenv("WT_STATE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "wt-state")
	}
	return filepath.Join(home, ".local", "state", "wt")
}

// acquireLock takes an exclusive, non-blocking flock on StateDir()/name.
// flock locks belong to the open file description, so a second open in the
// same process conflicts exactly like a second process would. ok is false
// when someone else holds it.
func acquireLock(name string) (release func(), ok bool, err error) {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true, nil
}

// LogReap appends one line to reap.log: time, path, outcome ("reaped" or
// the error), and the reason (spec §3.4). The first three columns keep
// their old meaning, so existing greps still work.
func LogReap(worktree string, err error, reason string) {
	dir := StateDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "reap.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer f.Close()
	outcome := "reaped"
	if err != nil {
		outcome = err.Error()
	}
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339), worktree, outcome, reason)
}
```

Rewrite `sweep.go`'s `Sweep` (keep `SweepOptions`):

```go
// Sweep inspects every marked ephemeral worktree of every configured repo
// and, unless DryRun, reaps the done ones. Only one sweep runs at a time;
// a second returns ErrSweepBusy immediately (spec §3.4).
//
// Repos are walked in name order so dry-run output is stable.
func Sweep(ctx context.Context, cfg config.Config, primaries map[string]string, opts SweepOptions) ([]Decision, error) {
	release, ok, err := acquireLock(sweepLockName)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSweepBusy
	}
	defer release()

	names := make([]string, 0, len(primaries))
	for n := range primaries {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []Decision
	for _, name := range names {
		primary := primaries[name]
		r, ok := cfg.Repos[name]
		if !ok || !r.EphemeralEnabled() {
			continue
		}
		root := filepath.Join(primary, r.EphemeralDirOrDefault())
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // no ephemeral dir yet is the normal case
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name())
			if _, err := ReadMarker(path); err != nil {
				continue // not wt's to remove
			}
			io := InspectOptions{Grace: opts.Grace}
			if opts.DryRun {
				out = append(out, Inspect(ctx, cfg, path, io))
				continue
			}
			d, err := ReapWith(ctx, cfg, path, io)
			LogReap(path, err, d.Reason)
			out = append(out, d)
		}
	}
	return out, nil
}
```

(imports: `context`, `os`, `path/filepath`, `sort`, `time`, config.)

In `cmd/wt/main.go`'s `case "reap"`, change `ephemeral.LogReap(root, err)` to:

```go
		d, err := ephemeral.ReapWith(ctx, cfg, root, ephemeral.InspectOptions{ExcludeSessionID: endingSession})
		ephemeral.LogReap(root, err, d.Reason)
```

(replacing the `err = ephemeral.Reap(...)` line above it). Task 9 moves this block into `cmd/wt/reap.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/ephemeral/ ./cmd/wt/ -v 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ephemeral cmd/wt/main.go
git commit -m "feat(ephemeral): exclusive sweeps, dry-run decisions, reason column in reap.log"
```

---

### Task 9: `wt reap --all [--dry-run] [--json] [--quiet]`

**Files:**
- Create: `cmd/wt/reap.go`
- Modify: `cmd/wt/main.go` (`case "reap"` becomes one line)
- Test: `cmd/wt/reap_test.go` (create)

**Interfaces:**
- Consumes: `ephemeral.Sweep`, `ReapWith`, `Inspect`, `ErrSweepBusy`, `DefaultSweepGrace`, `LogReap`, `ParseSessionEnd`, `ShouldReap`, `WorktreeRoot`, `ReadMarker`.
- Produces: `func runReap(cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wt/ -run TestReap -v`
Expected: FAIL — `undefined: runReap`.

- [ ] **Step 3: Implement**

`cmd/wt/reap.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/resolve"
)

// sweepTimeout bounds `wt reap --all`. Each worktree costs a process
// snapshot plus a few git calls, and a fleet can hold dozens.
const sweepTimeout = 5 * time.Minute

const reapUsage = "usage: wt reap [<path> | --all] [--dry-run] [--json] [--quiet]"

// runReap implements every form of `wt reap` and returns the exit code.
//
//	wt reap                      SessionEnd hook: payload on stdin
//	wt reap <path>               one worktree, no grace
//	wt reap --all                every marked ephemeral, with the sweep grace
//	--dry-run                    decide only; delete nothing
//	--json                       decisions as a JSON array on stdout
//	--quiet                      no output except errors (background sweeps)
func runReap(cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var all, dryRun, asJSON, quiet bool
	target := ""
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		case "--dry-run":
			dryRun = true
		case "--json":
			asJSON = true
		case "--quiet":
			quiet = true
		default:
			if strings.HasPrefix(a, "-") || target != "" {
				fmt.Fprintln(stderr, reapUsage)
				return 2
			}
			target = a
		}
	}
	if all && target != "" {
		fmt.Fprintln(stderr, reapUsage)
		return 2
	}

	if all {
		ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
		defer cancel()
		ds, err := ephemeral.Sweep(ctx, cfg, resolve.Primaries(cfg),
			ephemeral.SweepOptions{DryRun: dryRun, Grace: ephemeral.DefaultSweepGrace})
		if errors.Is(err, ephemeral.ErrSweepBusy) {
			if !quiet {
				fmt.Fprintln(stderr, "wt: reap: another sweep is already running; skipped")
			}
			return 0
		}
		if err != nil {
			fmt.Fprintln(stderr, "wt: reap:", err)
			return 1
		}
		return printDecisions(stdout, stderr, ds, dryRun, asJSON, quiet)
	}

	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	// endingSession is the one session the predicate must not count as
	// live: a SessionEnd hook runs while its own session is still alive.
	endingSession := ""
	if target == "" {
		ev, err := ephemeral.ParseSessionEnd(stdin)
		if err != nil {
			fmt.Fprintln(stderr, "wt: reap:", err)
			return 1
		}
		if !ephemeral.ShouldReap(ev.Reason) {
			return 0
		}
		target, endingSession = ev.Cwd, ev.SessionID
	}
	root, err := ephemeral.WorktreeRoot(ctx, target)
	if err != nil {
		return 0 // the hook fires for every session; most are not ours
	}
	if _, err := ephemeral.ReadMarker(root); err != nil {
		return 0 // not one of ours; say nothing
	}
	opts := ephemeral.InspectOptions{ExcludeSessionID: endingSession}
	if dryRun {
		return printDecisions(stdout, stderr, []ephemeral.Decision{ephemeral.Inspect(ctx, cfg, root, opts)}, true, asJSON, quiet)
	}
	d, err := ephemeral.ReapWith(ctx, cfg, root, opts)
	ephemeral.LogReap(root, err, d.Reason)
	if err != nil {
		fmt.Fprintln(stderr, "wt: reap:", err)
		return 1
	}
	if !quiet {
		fmt.Fprintln(stdout, "reaped", root)
	}
	return 0
}

// printDecisions writes ds as JSON or as one aligned line per worktree.
func printDecisions(stdout, stderr io.Writer, ds []ephemeral.Decision, dryRun, asJSON, quiet bool) int {
	if asJSON {
		if ds == nil {
			ds = []ephemeral.Decision{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(ds); err != nil {
			fmt.Fprintln(stderr, "wt: reap:", err)
			return 1
		}
		return 0
	}
	if quiet {
		return 0
	}
	for _, d := range ds {
		verb := map[bool]string{true: "reaped", false: "kept"}[d.Reap]
		if dryRun {
			verb = map[bool]string{true: "reap", false: "keep"}[d.Reap]
		}
		fmt.Fprintf(stdout, "%-6s %s  %s\n", verb, d.Path, d.Reason)
	}
	return 0
}
```

In `cmd/wt/main.go`, replace the whole `case "reap":` body with:

```go
	case "reap":
		os.Exit(runReap(cfg, os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./cmd/wt/ -v 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/reap.go cmd/wt/reap_test.go cmd/wt/main.go
git commit -m "feat(cli): wt reap --all with --dry-run, --json and --quiet"
```

---

### Task 10: Background sweep after any command

**Files:**
- Create: `internal/ephemeral/trigger.go`, `cmd/wt/autosweep.go`
- Modify: `cmd/wt/main.go` (one call after the command word is known)
- Test: `internal/ephemeral/trigger_test.go`, `cmd/wt/autosweep_test.go`

**Interfaces:**
- Consumes: `StateDir` (Task 8), `cfg.EffectiveReapTriggerInterval` (Task 1).
- Produces: `func TriggerDue(now time.Time, interval time.Duration) bool`; `func maybeSpawnSweep(cfg config.Config, cmd string)`; `func spawnsSweep(cmd string) bool`.

- [ ] **Step 1: Write the failing tests**

`internal/ephemeral/trigger_test.go`:

```go
package ephemeral_test

import (
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
)

func TestTriggerDueRateLimits(t *testing.T) {
	t.Setenv("WT_STATE_DIR", t.TempDir())
	now := time.Now()
	if !ephemeral.TriggerDue(now, 10*time.Minute) {
		t.Fatal("first call must be due")
	}
	if ephemeral.TriggerDue(now.Add(time.Minute), 10*time.Minute) {
		t.Fatal("a call within the interval must not be due")
	}
	if !ephemeral.TriggerDue(now.Add(11*time.Minute), 10*time.Minute) {
		t.Fatal("a call after the interval must be due")
	}
}
```

`cmd/wt/autosweep_test.go`:

```go
package main

import "testing"

func TestSpawnsSweep(t *testing.T) {
	for cmd, want := range map[string]bool{
		"status": true, "new": true, "open": true, "feat/x": true, "1234": true,
		"reap": false, "hook": false, "": false, "help": false, "timer": false,
	} {
		if got := spawnsSweep(cmd); got != want {
			t.Errorf("spawnsSweep(%q) = %v, want %v", cmd, got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ephemeral/ ./cmd/wt/ -run 'TriggerDue|SpawnsSweep' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`internal/ephemeral/trigger.go`:

```go
package ephemeral

import (
	"os"
	"path/filepath"
	"time"
)

// TriggerDue reports whether a background sweep is due, recording now as
// the last trigger when it is (spec §3.2). Two wt commands racing here can
// both see "due"; that only costs a second sweep, which exits on the sweep
// lock.
func TriggerDue(now time.Time, interval time.Duration) bool {
	p := filepath.Join(StateDir(), "last-sweep")
	if st, err := os.Stat(p); err == nil && now.Sub(st.ModTime()) < interval {
		return false
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return false
	}
	if os.WriteFile(p, nil, 0o600) != nil {
		return false
	}
	_ = os.Chtimes(p, now, now)
	return true
}
```

`cmd/wt/autosweep.go`:

```go
package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
)

// spawnsSweep reports whether running cmd should kick a background sweep.
// reap and hook ARE reaping; the dashboard ("") sweeps on its own refresh;
// help and timer are not worktree work.
func spawnsSweep(cmd string) bool {
	switch cmd {
	case "reap", "hook", "", "help", "-h", "--help", "timer":
		return false
	}
	return true
}

// maybeSpawnSweep starts a detached `wt reap --all --quiet` when one is due.
// Setsid detaches it from the terminal, so the foreground command never
// waits and Ctrl-C does not kill it. The child applies the sweep grace, so
// the worktree this very command may be creating is safe.
func maybeSpawnSweep(cfg config.Config, cmd string) {
	if !spawnsSweep(cmd) || !ephemeral.TriggerDue(time.Now(), cfg.EffectiveReapTriggerInterval()) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.Command(exe, "reap", "--all", "--quiet")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() == nil {
		_ = c.Process.Release()
	}
}
```

In `cmd/wt/main.go`, right after `cmd` is computed (before `switch cmd {`):

```go
	maybeSpawnSweep(cfg, cmd)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/ephemeral/ ./cmd/wt/ -v 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ephemeral/trigger.go internal/ephemeral/trigger_test.go cmd/wt/autosweep.go cmd/wt/autosweep_test.go cmd/wt/main.go
git commit -m "feat: rate-limited background sweep after any wt command"
```

---

### Task 11: Record `start` and `last_opened_at`

**Files:**
- Modify: `cmd/wt/main.go` (`openBranch`, `openOrCreateBranch`, `handOff`; extract `openBranchIn`)
- Test: `cmd/wt/lifecycle_marker_test.go` (create)

**Interfaces:**
- Consumes: `ephemeral.Marker` v2 fields, `ephemeral.TouchOpened` (Task 2).
- Produces: `func headOf(ctx context.Context, dir string) (string, error)`; `func touchIfEphemeral(path string)`; `func openBranchIn(cfg config.Config, primaries map[string]string, branch string) (string, error)` (Task 14 uses it).

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wt/ -run 'RecordsStart|NotWTCreated|TouchesAn' -v`
Expected: FAIL — `Start = ""`.

- [ ] **Step 3: Implement**

Add to `cmd/wt/main.go`:

```go
// headOf returns the commit dir's HEAD points at.
func headOf(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("reading HEAD of %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// touchIfEphemeral records that the user is being handed path, when path
// is a wt ephemeral. Best effort: a failure only means the idle clock
// starts from an older signal.
func touchIfEphemeral(path string) {
	if _, err := ephemeral.ReadMarker(path); err == nil {
		_ = ephemeral.TouchOpened(path, time.Now())
	}
}
```

At the top of `handOff`, before the herdr check:

```go
	touchIfEphemeral(path)
```

Rename `openBranch`'s body into `openBranchIn(cfg config.Config, primaries map[string]string, branch string) (string, error)`, deleting its own `primaries := resolve.Primaries(cfg)` line, and make `openBranch` a wrapper:

```go
func openBranch(cfg config.Config, branch string) (string, error) {
	return openBranchIn(cfg, resolve.Primaries(cfg), branch)
}
```

In `openBranchIn`, replace the `ephemeral.WriteMarker(target, ephemeral.Marker{...})` call with:

```go
	start, err := headOf(ctx, target)
	if err != nil {
		return "", fmt.Errorf("created %s but %w", target, err)
	}
	if err := ephemeral.WriteMarker(target, ephemeral.Marker{
		Repo:      m.Repo,
		Branch:    branch,
		Primary:   m.Primary,
		CreatedAt: time.Now(),
		Start:     start,
		// A remote-only branch gets its local copy from wt here; a local
		// branch is the user's own.
		BranchCreatedByWT: !m.Local,
	}); err != nil {
```

In `openOrCreateBranch`, do the same with `BranchCreatedByWT: false` (the user asked for a brand-new branch; it holds their work, and §4.2 must not delete it).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/wt/ -v 2>&1 | tail -20`
Expected: PASS (new and existing `openBranch` tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/main.go cmd/wt/lifecycle_marker_test.go
git commit -m "feat: record start commit and last-opened time on ephemerals"
```

---

### Task 12: `wt hooks install claude`

**Files:**
- Create: `cmd/wt/hooks.go`
- Modify: `cmd/wt/main.go` (dispatch `hooks`)
- Test: `cmd/wt/hooks_test.go`

**Interfaces:**
- Produces: `func mergeSessionEndHook(body []byte, cmdline string) ([]byte, bool, error)`; `func claudeSettingsPath() string`; `func runHooks(args []string, stdin io.Reader, stdout, stderr io.Writer) int`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const existingSettings = `{
  "model": "opus",
  "hooks": {
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "/x/portwatch hook end", "timeout": 10}]}
    ]
  }
}`

func TestMergeSessionEndHookAppendsAndPreservesOthers(t *testing.T) {
	out, changed, err := mergeSessionEndHook([]byte(existingSettings), "/bin/wt reap")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatal(err)
	}
	if s["model"] != "opus" {
		t.Error("unrelated keys must survive")
	}
	groups := s["hooks"].(map[string]any)["SessionEnd"].([]any)
	if len(groups) != 2 {
		t.Fatalf("want the portwatch group plus ours, got %d groups", len(groups))
	}
	if !strings.Contains(string(out), "portwatch hook end") || !strings.Contains(string(out), "/bin/wt reap") {
		t.Errorf("output lost a hook:\n%s", out)
	}
}

func TestMergeSessionEndHookIsIdempotent(t *testing.T) {
	once, _, _ := mergeSessionEndHook([]byte(existingSettings), "/bin/wt reap")
	_, changed, err := mergeSessionEndHook(once, "/other/path/wt reap")
	if err != nil || changed {
		t.Fatalf("a second install must be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestMergeSessionEndHookOnEmptyFile(t *testing.T) {
	out, changed, err := mergeSessionEndHook(nil, "/bin/wt reap")
	if err != nil || !changed || !strings.Contains(string(out), `"SessionEnd"`) {
		t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
	}
}

func TestMergeSessionEndHookRefusesMalformed(t *testing.T) {
	for _, body := range []string{`{not json`, `{"hooks": []}`, `{"hooks": {"SessionEnd": {}}}`} {
		if _, _, err := mergeSessionEndHook([]byte(body), "/bin/wt reap"); err == nil {
			t.Errorf("%s: want an error, never an overwrite", body)
		}
	}
}

func TestRunHooksInstallWithYesWritesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(existingSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runHooks([]string{"install", "claude", "--yes"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), " reap") {
		t.Errorf("settings.json not updated:\n%s", body)
	}
	if bak, err := os.ReadFile(p + ".wt-backup"); err != nil || string(bak) != existingSettings {
		t.Errorf("backup missing or wrong: %v", err)
	}
}

func TestRunHooksInstallWithoutTTYOrYesRefuses(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := runHooks([]string{"install", "claude"}, strings.NewReader(""), &out, &errb); code == 0 {
		t.Fatal("must refuse to edit settings unattended without --yes")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wt/ -run 'Hook' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`cmd/wt/hooks.go`:

```go
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/theczechr/ephemera/internal/ui"
)

const hooksUsage = "usage: wt hooks install claude [--yes]"

// claudeSettingsPath is $CLAUDE_CONFIG_DIR/settings.json, else
// ~/.claude/settings.json.
func claudeSettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// isWtReap matches a hook command that already runs `<anything>/wt reap`.
func isWtReap(cmd string) bool {
	f := strings.Fields(cmd)
	return len(f) == 2 && filepath.Base(f[0]) == "wt" && f[1] == "reap"
}

// mergeSessionEndHook adds a SessionEnd command hook running cmdline to a
// Claude Code settings.json body. It returns changed=false when a `wt reap`
// hook is already present. A body whose hooks are not the documented shape
// is an error: guessing would risk dropping the user's other hooks.
//
// Keys are re-serialised in sorted order (encoding/json), so the file's
// key order may change; its content does not.
func mergeSessionEndHook(body []byte, cmdline string) ([]byte, bool, error) {
	settings := map[string]any{}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &settings); err != nil {
			return nil, false, fmt.Errorf("settings.json is not valid JSON: %w", err)
		}
	}
	hooks := map[string]any{}
	if raw, ok := settings["hooks"]; ok {
		h, ok := raw.(map[string]any)
		if !ok {
			return nil, false, errors.New(`settings.json "hooks" is not an object`)
		}
		hooks = h
	}
	var groups []any
	if raw, ok := hooks["SessionEnd"]; ok {
		g, ok := raw.([]any)
		if !ok {
			return nil, false, errors.New(`settings.json "hooks.SessionEnd" is not a list`)
		}
		groups = g
	}
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); isWtReap(c) {
				return body, false, nil
			}
		}
	}
	groups = append(groups, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": cmdline}},
	})
	hooks["SessionEnd"] = groups
	settings["hooks"] = hooks
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

// runHooks implements `wt hooks install claude [--yes]`. It never edits the
// file without consent: on a terminal it asks; without one, --yes is
// required. The previous file is kept as settings.json.wt-backup.
func runHooks(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	yes := false
	var rest []string
	for _, a := range args {
		if a == "--yes" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 2 || rest[0] != "install" || rest[1] != "claude" {
		fmt.Fprintln(stderr, hooksUsage)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	cmdline := exe + " reap"
	path := claudeSettingsPath()
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	out, changed, err := mergeSessionEndHook(body, cmdline)
	if err != nil {
		fmt.Fprintf(stderr, "wt: hooks: %s: %v\n", path, err)
		return 1
	}
	if !changed {
		fmt.Fprintf(stdout, "a `wt reap` SessionEnd hook is already in %s\n", path)
		return 0
	}
	fmt.Fprintf(stdout, "adds to %s:\n  SessionEnd → %s\n", path, cmdline)
	if !yes {
		if !ui.Interactive() {
			fmt.Fprintln(stderr, "wt: hooks: not a terminal; rerun with --yes to apply")
			return 1
		}
		fmt.Fprint(stdout, "apply? [y/N] ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "left unchanged")
			return 0
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	if body != nil {
		if err := os.WriteFile(path+".wt-backup", body, 0o600); err != nil {
			fmt.Fprintln(stderr, "wt: hooks: writing backup:", err)
			return 1
		}
	}
	tmp := path + ".wt-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	fmt.Fprintln(stdout, "installed; previous file saved as", path+".wt-backup")
	return 0
}
```

In `main.go`, add a case:

```go
	case "hooks":
		os.Exit(runHooks(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/wt/ -run Hook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/hooks.go cmd/wt/hooks_test.go cmd/wt/main.go
git commit -m "feat(cli): wt hooks install claude"
```

---

### Task 13: `wt timer install|uninstall|status`

**Files:**
- Create: `cmd/wt/timer.go`
- Modify: `cmd/wt/main.go` (dispatch `timer`)
- Test: `cmd/wt/timer_test.go`

**Interfaces:**
- Produces: `const timerLabel = "com.theczechr.ephemera.reap"`; `func launchdPlist(exe string) string`; `func systemdService(exe string) string`; `func systemdTimer() string`; `func runTimer(args []string, stdout, stderr io.Writer) int`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestLaunchdPlistRunsAnHourlySweep(t *testing.T) {
	p := launchdPlist("/Users/a b/.local/bin/wt")
	for _, want := range []string{
		"<string>" + timerLabel + "</string>",
		"<string>/Users/a b/.local/bin/wt</string>",
		"<string>reap</string>", "<string>--all</string>", "<string>--quiet</string>",
		"<key>StartInterval</key>", "<integer>3600</integer>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if err := xml.Unmarshal([]byte(p), new(struct{})); err != nil {
		t.Errorf("plist is not well-formed XML: %v", err)
	}
}

func TestLaunchdPlistEscapesThePath(t *testing.T) {
	if p := launchdPlist("/tmp/a&b/wt"); !strings.Contains(p, "/tmp/a&amp;b/wt") {
		t.Errorf("an & in the path must be escaped:\n%s", p)
	}
}

func TestSystemdUnits(t *testing.T) {
	svc := systemdService("/home/a/.local/bin/wt")
	if !strings.Contains(svc, `ExecStart="/home/a/.local/bin/wt" reap --all --quiet`) || !strings.Contains(svc, "Type=oneshot") {
		t.Errorf("service:\n%s", svc)
	}
	tmr := systemdTimer()
	if !strings.Contains(tmr, "OnUnitActiveSec=1h") || !strings.Contains(tmr, "WantedBy=timers.target") {
		t.Errorf("timer:\n%s", tmr)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wt/ -run 'Launchd|Systemd' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`cmd/wt/timer.go`:

```go
package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

const (
	timerLabel = "com.theczechr.ephemera.reap"
	timerUnit  = "wt-reap"
	timerUsage = "usage: wt timer install|uninstall|status"
)

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// launchdPlist is the LaunchAgent that runs an hourly sweep (spec §3.3).
func launchdPlist(exe string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + timerLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlEscape(exe) + `</string>
    <string>reap</string>
    <string>--all</string>
    <string>--quiet</string>
  </array>
  <key>StartInterval</key>
  <integer>3600</integer>
  <key>RunAtLoad</key>
  <false/>
</dict>
</plist>
`
}

func systemdService(exe string) string {
	return "[Unit]\nDescription=wt: reap finished ephemeral worktrees\n\n" +
		"[Service]\nType=oneshot\nExecStart=\"" + exe + "\" reap --all --quiet\n"
}

func systemdTimer() string {
	return "[Unit]\nDescription=Hourly wt reap\n\n" +
		"[Timer]\nOnBootSec=15min\nOnUnitActiveSec=1h\n\n" +
		"[Install]\nWantedBy=timers.target\n"
}

func run(stderr io.Writer, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stderr = stderr
	return c.Run()
}

// runTimer manages the optional hourly sweep. macOS uses a LaunchAgent,
// Linux a systemd user timer; anything else is refused.
func runTimer(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, timerUsage)
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "wt: timer:", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "wt: timer:", err)
		return 1
	}
	switch runtime.GOOS {
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", timerLabel+".plist")
		domain := "gui/" + strconv.Itoa(os.Getuid())
		switch args[0] {
		case "install":
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err == nil {
				err = os.WriteFile(plist, []byte(launchdPlist(exe)), 0o644)
			}
			if err != nil {
				fmt.Fprintln(stderr, "wt: timer:", err)
				return 1
			}
			_ = exec.Command("launchctl", "bootout", domain+"/"+timerLabel).Run() // reinstall is idempotent
			if err := run(stderr, "launchctl", "bootstrap", domain, plist); err != nil {
				fmt.Fprintln(stderr, "wt: timer: launchctl bootstrap:", err)
				return 1
			}
			fmt.Fprintln(stdout, "installed: hourly `wt reap --all` via", plist)
		case "uninstall":
			_ = exec.Command("launchctl", "bootout", domain+"/"+timerLabel).Run()
			if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
				fmt.Fprintln(stderr, "wt: timer:", err)
				return 1
			}
			fmt.Fprintln(stdout, "uninstalled")
		case "status":
			if exec.Command("launchctl", "print", domain+"/"+timerLabel).Run() == nil {
				fmt.Fprintln(stdout, "installed:", plist)
			} else {
				fmt.Fprintln(stdout, "not installed")
			}
		default:
			fmt.Fprintln(stderr, timerUsage)
			return 2
		}
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		svc, tmr := filepath.Join(dir, timerUnit+".service"), filepath.Join(dir, timerUnit+".timer")
		switch args[0] {
		case "install":
			err := os.MkdirAll(dir, 0o755)
			if err == nil {
				err = os.WriteFile(svc, []byte(systemdService(exe)), 0o644)
			}
			if err == nil {
				err = os.WriteFile(tmr, []byte(systemdTimer()), 0o644)
			}
			if err == nil {
				err = run(stderr, "systemctl", "--user", "daemon-reload")
			}
			if err == nil {
				err = run(stderr, "systemctl", "--user", "enable", "--now", timerUnit+".timer")
			}
			if err != nil {
				fmt.Fprintln(stderr, "wt: timer:", err)
				return 1
			}
			fmt.Fprintln(stdout, "installed: hourly `wt reap --all` via", tmr)
		case "uninstall":
			_ = exec.Command("systemctl", "--user", "disable", "--now", timerUnit+".timer").Run()
			for _, p := range []string{svc, tmr} {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					fmt.Fprintln(stderr, "wt: timer:", err)
					return 1
				}
			}
			_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
			fmt.Fprintln(stdout, "uninstalled")
		case "status":
			if exec.Command("systemctl", "--user", "is-enabled", "--quiet", timerUnit+".timer").Run() == nil {
				fmt.Fprintln(stdout, "installed:", tmr)
			} else {
				fmt.Fprintln(stdout, "not installed")
			}
		default:
			fmt.Fprintln(stderr, timerUsage)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "wt: timer: %s is not supported; run `wt reap --all` from your own scheduler\n", runtime.GOOS)
		return 1
	}
	return 0
}
```

In `main.go`:

```go
	case "timer":
		os.Exit(runTimer(os.Args[2:], os.Stdout, os.Stderr))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/wt/ -run 'Launchd|Systemd' -v`
Expected: PASS.

- [ ] **Step 5: Manual check (macOS)**

Run: `go build -o /tmp/wt ./cmd/wt && /tmp/wt timer status`
Expected: `not installed`. Do not run `install` here; the user decides that.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/timer.go cmd/wt/timer_test.go cmd/wt/main.go
git commit -m "feat(cli): wt timer install|uninstall|status"
```

---

### Task 14: `wt <PR>`

**Files:**
- Create: `cmd/wt/pr.go`
- Modify: `cmd/wt/main.go` (default arm dispatch)
- Test: `cmd/wt/pr_test.go`

**Interfaces:**
- Consumes: `openBranchIn`, `headOf`, `resolveRepo`, `pickOne`-style `ui.Choose` (existing); `ephemeral.OwnCommits`, `UpdateMarker`, `WriteMarker`, `EnsureExcluded`; `bootstrap.CreateAt`, `bootstrap.Slug`; `model.PRRemoteRef` (Task 6).
- Produces:

```go
type prRef struct {
	Owner, Name string // empty for a bare number
	Number      int
}
func parsePRArg(arg string) (prRef, bool)
type ghPR struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	HeadRefName       string `json:"headRefName"`
	HeadRefOid        string `json:"headRefOid"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	URL               string `json:"url"`
}
func ghPRView(ctx context.Context, primary string, n int) (ghPR, error)
func originMatches(remoteURL, owner, repo string) bool
func repoForPR(cfg config.Config, ref prRef, cwd string) (repoName, primary string, err error)
func openPR(cfg config.Config, ref prRef, cwd string, stderr io.Writer) (string, error)
type usageError struct{ msg string }
```

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestParsePRArg(t *testing.T) {
	cases := map[string]prRef{
		"1234":  {Number: 1234},
		"#1234": {Number: 1234},
		"https://github.com/acme/server/pull/77":       {Owner: "acme", Name: "server", Number: 77},
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
	t.Setenv("PATH", t.TempDir())
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wt/ -run 'PR|Origin|Numeric' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`cmd/wt/pr.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theczechr/ephemera/internal/bootstrap"
	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/model"
	"github.com/theczechr/ephemera/internal/resolve"
	"github.com/theczechr/ephemera/internal/ui"
)

type prRef struct {
	Owner, Name string
	Number      int
}

var (
	prNumberArg = regexp.MustCompile(`^#?([0-9]+)$`)
	prURLArg    = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/([0-9]+)(?:[/?#].*)?$`)
)

// parsePRArg recognises `1234`, `#1234` and a github.com PR URL (spec
// §5.1). A bare number is always a PR; `wt open 1234` reaches a branch.
func parsePRArg(arg string) (prRef, bool) {
	if m := prNumberArg.FindStringSubmatch(arg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return prRef{Number: n}, n > 0
	}
	if m := prURLArg.FindStringSubmatch(arg); m != nil {
		n, _ := strconv.Atoi(m[3])
		return prRef{Owner: m[1], Name: m[2], Number: n}, n > 0
	}
	return prRef{}, false
}

type ghPR struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	HeadRefName       string `json:"headRefName"`
	HeadRefOid        string `json:"headRefOid"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	URL               string `json:"url"`
}

// usageError makes openPRAndCd exit 2 instead of 1.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// ghPRView asks gh about PR n in the repo checked out at primary (spec §5.2).
func ghPRView(ctx context.Context, primary string, n int) (ghPR, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return ghPR{}, errors.New("opening a PR needs the GitHub CLI (gh) on PATH: https://cli.github.com")
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(n), "--json",
		"number,state,headRefName,headRefOid,isCrossRepository,headRepositoryOwner,url")
	cmd.Dir = primary
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return ghPR{}, fmt.Errorf("gh pr view %d: %w: %s", n, err, bytes.TrimSpace(stderr.Bytes()))
	}
	var p ghPR
	if err := json.Unmarshal(out, &p); err != nil || p.HeadRefName == "" || p.HeadRefOid == "" {
		return ghPR{}, fmt.Errorf("gh pr view %d: unexpected output %q", n, bytes.TrimSpace(out))
	}
	return p, nil
}

// originMatches reports whether remoteURL points at github.com/<owner>/<repo>,
// in SSH or HTTPS form, ignoring case, a trailing .git and a trailing slash.
func originMatches(remoteURL, owner, repo string) bool {
	u := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(remoteURL), "/"), ".git"))
	want := strings.ToLower(owner + "/" + repo)
	return strings.HasSuffix(u, ":"+want) || strings.HasSuffix(u, "/"+want)
}

// repoForPR picks the configured repo a PR belongs to (spec §5.1).
func repoForPR(cfg config.Config, ref prRef, cwd string) (string, string, error) {
	primaries := resolve.Primaries(cfg)
	if ref.Owner != "" {
		for name, primary := range primaries {
			out, err := exec.Command("git", "-C", primary, "remote", "get-url",
				cfg.Repos[name].RemoteOrDefault()).Output()
			if err == nil && originMatches(string(out), ref.Owner, ref.Name) {
				return name, primary, nil
			}
		}
		return "", "", fmt.Errorf("no configured repo has %s/%s as its remote", ref.Owner, ref.Name)
	}
	if name, primary, err := resolveRepo(cfg, cwd); err == nil {
		return name, primary, nil
	}
	names := repoNames(primaries)
	if len(names) == 0 {
		return "", "", fmt.Errorf("no configured repo found under any scan root %v", cfg.ScanRoots)
	}
	if !ui.Interactive() {
		return "", "", usageError{fmt.Sprintf(
			"PR #%d: run this inside a repo, or pass the PR URL; configured repos: %s",
			ref.Number, strings.Join(names, ", "))}
	}
	idx, err := ui.Choose(fmt.Sprintf("which repo is PR #%d in?", ref.Number), names)
	if err != nil {
		return "", "", err
	}
	if idx < 0 {
		return "", "", errors.New("cancelled")
	}
	return names[idx], primaries[names[idx]], nil
}

// worktreeFor returns the worktree that has branch checked out, or "".
func worktreeFor(ctx context.Context, primary, branch string) string {
	ws, err := discover.Worktrees(ctx, primary, "")
	if err != nil {
		return ""
	}
	for _, w := range ws {
		if w.Branch == branch {
			return w.Path
		}
	}
	return ""
}

// fetchPR fetches PR head into refs/remotes/wt-pr/<n> (see model.PRRemoteRef).
func fetchPR(ctx context.Context, primary, remote string, n int) error {
	spec := fmt.Sprintf("+refs/pull/%d/head:%s", n, model.PRRemoteRef(n))
	out, err := exec.CommandContext(ctx, "git", "-C", primary, "fetch", "-q", remote, spec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetching PR #%d: %w: %s", n, err, bytes.TrimSpace(out))
	}
	return nil
}

// openPR resolves a PR to a worktree path, creating or updating it.
func openPR(cfg config.Config, ref prRef, cwd string, stderr io.Writer) (string, error) {
	repoName, primary, err := repoForPR(cfg, ref, cwd)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
	defer cancel()
	pr, err := ghPRView(ctx, primary, ref.Number)
	if err != nil {
		return "", err
	}
	r := cfg.Repos[repoName]
	remote := r.RemoteOrDefault()
	branch := pr.HeadRefName
	if pr.IsCrossRepository {
		branch = fmt.Sprintf("pr/%d", pr.Number)
	}

	if existing := worktreeFor(ctx, primary, branch); existing != "" {
		updateToPRHead(ctx, existing, primary, remote, pr, stderr)
		return existing, nil
	}

	if !pr.IsCrossRepository {
		path, err := openBranchIn(cfg, map[string]string{repoName: primary}, branch)
		if err != nil {
			return "", err
		}
		// Only an ephemeral has a marker; a permanent worktree stays as is.
		_ = ephemeral.UpdateMarker(path, func(m *ephemeral.Marker) { m.PR = pr.Number })
		return path, nil
	}

	if !r.EphemeralEnabled() {
		return "", fmt.Errorf("repo %q has ephemeral = false; fork PRs open only as ephemerals", repoName)
	}
	if err := fetchPR(ctx, primary, remote, pr.Number); err != nil {
		return "", err
	}
	created := false
	if !bootstrapBranchExists(ctx, primary, branch) {
		if out, err := exec.CommandContext(ctx, "git", "-C", primary, "branch", branch,
			model.PRRemoteRef(pr.Number)).CombinedOutput(); err != nil {
			return "", fmt.Errorf("creating %s: %w: %s", branch, err, bytes.TrimSpace(out))
		}
		created = true
	}
	target := filepath.Join(primary, r.EphemeralDirOrDefault(), bootstrap.Slug(branch))
	if err := ephemeral.EnsureExcluded(ctx, primary,
		[]string{r.EphemeralDirOrDefault() + "/", ephemeral.MarkerName}); err != nil {
		return "", err
	}
	if err := bootstrap.CreateAt(ctx, primary, target, branch, "", r.ForEphemeral()); err != nil {
		return "", fmt.Errorf("bootstrapping %s: %w (left in place for you to inspect or remove)", target, err)
	}
	start, err := headOf(ctx, target)
	if err != nil {
		return "", fmt.Errorf("created %s but %w", target, err)
	}
	if err := ephemeral.WriteMarker(target, ephemeral.Marker{
		Repo: repoName, Branch: branch, Primary: primary, CreatedAt: time.Now(),
		Start: start, PR: pr.Number, BranchCreatedByWT: created,
	}); err != nil {
		return "", fmt.Errorf("created %s but could not mark it ephemeral: %w", target, err)
	}
	return target, nil
}

// updateToPRHead brings an existing PR worktree to the PR's current head
// when that loses nothing: clean, and no commits of the user's own (spec
// §5.3). Otherwise it says how far behind the worktree is and leaves it.
func updateToPRHead(ctx context.Context, wt, primary, remote string, pr ghPR, stderr io.Writer) {
	if pr.IsCrossRepository {
		if err := fetchPR(ctx, primary, remote, pr.Number); err != nil {
			fmt.Fprintln(stderr, "wt:", err)
			return
		}
	} else {
		_ = exec.CommandContext(ctx, "git", "-C", primary, "fetch", "-q", remote, pr.HeadRefName).Run()
	}
	head, err := headOf(ctx, wt)
	if err != nil || head == pr.HeadRefOid {
		return
	}
	status, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", wt, "status", "--porcelain").Output()
	own := 1
	if m, merr := ephemeral.ReadMarker(wt); merr == nil {
		if n, oerr := ephemeral.OwnCommits(ctx, wt, m); oerr == nil {
			own = n
		}
	}
	if err == nil && len(bytes.TrimSpace(status)) == 0 && own == 0 {
		if out, err := exec.CommandContext(ctx, "git", "-C", wt, "merge", "--ff-only", "-q", pr.HeadRefOid).CombinedOutput(); err != nil {
			fmt.Fprintf(stderr, "wt: could not fast-forward to the PR head: %s\n", bytes.TrimSpace(out))
		}
		return
	}
	n := "?"
	if out, err := exec.CommandContext(ctx, "git", "-C", wt, "rev-list", "--count", "HEAD.."+pr.HeadRefOid).Output(); err == nil {
		n = strings.TrimSpace(string(out))
	}
	plural := "s"
	if n == "1" {
		plural = ""
	}
	fmt.Fprintf(stderr, "wt: PR #%d has %s new commit%s; your worktree has local work, so it was left as is\n",
		pr.Number, n, plural)
}

// openPRAndCd is the CLI entry for `wt <PR>`.
func openPRAndCd(cfg config.Config, ref prRef) {
	cwd, _ := os.Getwd()
	path, err := openPR(cfg, ref, cwd, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		var ue usageError
		if errors.As(err, &ue) {
			os.Exit(2)
		}
		os.Exit(1)
	}
	handOff(path, "")
}
```

`repoNames` (existing in main.go) returns the repo names sorted, so the picker and the error list are stable.

In `main.go`'s default arm, before `openAndCd(cfg, cmd)`:

```go
		if ref, ok := parsePRArg(cmd); ok {
			openPRAndCd(cfg, ref)
			return
		}
```

Update the "branch not found" error text in `openBranchIn` to mention PRs: `"If you meant a subcommand, the reserved words are: status, new, bootstrap, reap, open, hooks, timer, branches. A number opens a PR; use `wt open <name>` for a branch named like one."`

- [ ] **Step 4: Run tests to verify they pass**

Run: `go vet ./cmd/wt/ && go test ./cmd/wt/ -v 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/pr.go cmd/wt/pr_test.go cmd/wt/main.go
git commit -m "feat(cli): wt <PR> opens pull requests, including forks"
```

---

### Task 15: Unpushed branches without a worktree

**Files:**
- Create: `internal/discover/orphans.go`, `cmd/wt/branches.go`
- Modify: `internal/ui/ui.go`, `cmd/wt/main.go`
- Test: `internal/discover/orphans_test.go`, `cmd/wt/branches_test.go`, `internal/ui/orphans_test.go`

**Interfaces:**
- Produces: `func discover.UnpushedOrphans(ctx context.Context, primary string) ([]string, error)`; `func runBranches(cfg config.Config, args []string, stdout, stderr io.Writer) int`; `ui.Run(..., ops TrashOps, orphans func() int)`.

- [ ] **Step 1: Write the failing tests**

`internal/discover/orphans_test.go`:

```go
package discover_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestUnpushedOrphans(t *testing.T) {
	r := gittest.New(t)
	// local-only work, no worktree: listed
	r.Git(t, r.Primary, "checkout", "-q", "-b", "feat/orphan")
	r.WriteCommit(t, r.Primary, "o.txt", "x")
	r.Git(t, r.Primary, "checkout", "-q", "main")
	// pushed, no worktree: not listed
	r.PushNewBranch(t, "feat/pushed")
	r.Git(t, r.Primary, "fetch", "-q", "origin")
	r.Git(t, r.Primary, "branch", "-q", "feat/pushed", "origin/feat/pushed")
	// local-only work, checked out in a worktree: not listed
	wt := filepath.Join(r.Primary, ".worktrees", "busy")
	r.Git(t, r.Primary, "worktree", "add", "-q", "-b", "feat/busy", wt)
	r.WriteCommit(t, wt, "b.txt", "x")

	got, err := discover.UnpushedOrphans(context.Background(), r.Primary)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"feat/orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
```

`cmd/wt/branches_test.go`:

```go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestRunBranchesUnpushed(t *testing.T) {
	r := gittest.New(t)
	r.Git(t, r.Primary, "checkout", "-q", "-b", "feat/orphan")
	r.WriteCommit(t, r.Primary, "o.txt", "x")
	r.Git(t, r.Primary, "checkout", "-q", "main")
	var out, errb bytes.Buffer
	if code := runBranches(cfgFor(r, config.Repo{}), []string{"--unpushed"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := filepath.Base(r.Primary) + "\tfeat/orphan"
	if strings.TrimSpace(out.String()) != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}
```

`internal/ui/orphans_test.go`:

```go
package ui

import (
	"strings"
	"testing"
)

func TestFooterShowsUnpushedOrphanCount(t *testing.T) {
	m := withSessions(1)
	m.orphanCount = 3
	if v := m.View(); !strings.Contains(v, "3 local branches with unpushed commits and no worktree") {
		t.Errorf("footer does not show the count:\n%s", v)
	}
	m.orphanCount = 0
	if v := m.View(); strings.Contains(v, "unpushed commits and no worktree") {
		t.Error("no note when there are none")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/discover/ ./internal/ui/ ./cmd/wt/ -run 'Orphan|Branches' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`internal/discover/orphans.go`:

```go
package discover

import (
	"context"
	"os/exec"
	"strings"
)

// UnpushedOrphans lists local branches that hold commits on no remote and
// are not checked out in any worktree (spec §2.5). This is what keeps the
// reaper's "the branch survives" promise honest: work that survives only on
// a branch nobody looks at is still visible somewhere.
func UnpushedOrphans(ctx context.Context, primary string) ([]string, error) {
	ws, err := Worktrees(ctx, primary, "")
	if err != nil {
		return nil, err
	}
	checkedOut := map[string]bool{}
	for _, w := range ws {
		checkedOut[w.Branch] = true
	}
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary,
		"for-each-ref", "--format=%(refname:short)", "refs/heads/").Output()
	if err != nil {
		return nil, err
	}
	var res []string
	for _, b := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if b == "" || checkedOut[b] {
			continue
		}
		one, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary,
			"rev-list", "-n", "1", "refs/heads/"+b, "--not", "--remotes").Output()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(one)) != "" {
			res = append(res, b)
		}
	}
	return res, nil
}
```

`cmd/wt/branches.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/resolve"
)

// runBranches implements `wt branches --unpushed`: one "repo<TAB>branch"
// line per local branch with commits on no remote and no worktree.
func runBranches(cfg config.Config, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--unpushed" {
		fmt.Fprintln(stderr, "usage: wt branches --unpushed")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	primaries := resolve.Primaries(cfg)
	for _, name := range repoNames(primaries) {
		bs, err := discover.UnpushedOrphans(ctx, primaries[name])
		if err != nil {
			fmt.Fprintf(stderr, "wt: branches: %s: %v\n", name, err)
			return 1
		}
		for _, b := range bs {
			fmt.Fprintf(stdout, "%s\t%s\n", name, b)
		}
	}
	return 0
}

// countOrphans feeds the dashboard footer. Errors count as zero: it is a
// hint, and a failed probe must not paint a warning that isn't true.
func countOrphans(cfg config.Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	n := 0
	for _, primary := range resolve.Primaries(cfg) {
		if bs, err := discover.UnpushedOrphans(ctx, primary); err == nil {
			n += len(bs)
		}
	}
	return n
}
```

`internal/ui/ui.go`:

1. Add fields to `uiModel`:

```go
	// orphans counts local branches with unpushed commits and no worktree
	// (spec §2.5); orphanCount is its latest answer, shown in the footer.
	orphans     func() int
	orphanCount int
```

2. Add the message and command near `refreshedMsg`:

```go
// orphanCountMsg carries a completed orphans() call back into Update.
type orphanCountMsg struct{ count int }

func (m uiModel) orphanCmd() tea.Cmd {
	f := m.orphans
	return func() tea.Msg { return orphanCountMsg{count: f()} }
}
```

3. Replace `Init`:

```go
func (m uiModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.refreshing && m.refresh != nil {
		cmds = append(cmds, m.refreshCmd())
	}
	if m.orphans != nil {
		cmds = append(cmds, m.orphanCmd())
	}
	return tea.Batch(cmds...)
}
```

4. In `Update`, change the `refreshedMsg` case and add a case:

```go
	case refreshedMsg:
		m = m.applyRefresh(msg.ws, msg.unresolvedProcs)
		if m.orphans != nil {
			return m, m.orphanCmd()
		}
		return m, nil
	case orphanCountMsg:
		m.orphanCount = msg.count
		return m, nil
```

5. In `View`, change the `default:` footer arm:

```go
	default:
		footer = styleFor(roleDim, false).Render(m.footerKeys())
		if note := m.orphanNote(); note != "" {
			footer += "  " + styleFor(roleOrange, false).Render(note)
		}
```

and add:

```go
func (m uiModel) orphanNote() string {
	switch n := m.orphanCount; {
	case n <= 0:
		return ""
	case n == 1:
		return "⚠ 1 local branch with unpushed commits and no worktree (wt branches --unpushed)"
	default:
		return fmt.Sprintf("⚠ %d local branches with unpushed commits and no worktree (wt branches --unpushed)", n)
	}
}
```

6. Add `orphans func() int` as `Run`'s last parameter and `orphans: orphans,` to the `uiModel` literal.

`cmd/wt/main.go`: pass `func() int { return countOrphans(cfg) }` as the new last argument to `ui.Run`, and add:

```go
	case "branches":
		os.Exit(runBranches(cfg, os.Args[2:], os.Stdout, os.Stderr))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./... 2>&1 | tail -20`
Expected: PASS everywhere.

- [ ] **Step 5: Commit**

```bash
git add internal/discover/orphans.go internal/discover/orphans_test.go cmd/wt/branches.go cmd/wt/branches_test.go internal/ui cmd/wt/main.go
git commit -m "feat: surface unpushed branches that have no worktree"
```

---

### Task 16: Reserved words, usage, README, end-to-end check

**Files:**
- Modify: `cmd/wt/main.go` (`reservedCommands`, `usage`)
- Modify: `cmd/wt/main_test.go` (`TestIsReservedCommand`)
- Modify: `README.md`

- [ ] **Step 1: Write the failing test**

In `TestIsReservedCommand`, add `"hooks"`, `"timer"`, `"branches"` to the reserved cases.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/wt/ -run TestIsReservedCommand -v`
Expected: FAIL for the three new words.

- [ ] **Step 3: Implement**

```go
var reservedCommands = map[string]bool{
	"status": true, "new": true, "bootstrap": true, "reap": true,
	"open": true, "hook": true, "hooks": true, "timer": true, "branches": true,
	"help": true, "-h": true, "--help": true,
}

const usage = "usage: wt [status|new|bootstrap|open <branch>|<branch>|<PR>|" +
	"reap [<path>|--all] [--dry-run] [--json]|hooks install claude|timer install|uninstall|status|" +
	"branches --unpushed|hook <name>]"
```

README: add a "Cleanup" section covering, in this order: the done rule (no own commits → removed when safe; own commits → PR merged/closed or `idle_days`, default 3); the 15-minute sweep grace and why; what is never removed (uncommitted or untracked changes incl. submodules, live sessions, running processes, `git worktree lock`ed worktrees, `wt new` worktrees); that branches survive except wt-created ones fully on a remote; `wt reap --all --dry-run` to see why something is still there; `wt hooks install claude`; `wt timer install`; `wt branches --unpushed`. Add a "Pull requests" section: `wt 1234`, `wt '#1234'` (zsh), PR URLs, fork PRs as `pr/<N>`, re-running fast-forwards a clean review worktree, `wt open 1234` for a branch named `1234`. Document `idle_days` and `reap_trigger_interval` in the config table. Replace any remaining "never passes --force" / "wt never deletes anything" wording with the narrowed rule.

- [ ] **Step 4: Full verification**

Run:

```bash
gofmt -l . && go vet ./... && go test ./...
go build -o /tmp/wt-lifecycle ./cmd/wt
WT_STATE_DIR=$(mktemp -d) /tmp/wt-lifecycle reap --all --dry-run
```

Expected: tests green. The dry run (read-only; it deletes nothing) prints one `reap` or `keep` line per marked ephemeral across the configured repos. Every `upc-web/.worktrees/*` entry has a concrete reason, and no line says "branch has no upstream" or "submodules cannot be moved" (spec §8).

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/main.go cmd/wt/main_test.go README.md
git commit -m "docs: cleanup and pull-request workflows; reserve new subcommands"
```

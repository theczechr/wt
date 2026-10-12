# Lifecycle core — worktrees that actually clean up after themselves

Status: approved design, not yet implemented
Date: 2026-10-12

## Problem

ephemera's promise is the review loop: `wt <branch>` drops you into a ready
worktree, you review, you forget about it, and it disappears. The first two
steps work. The last one mostly does not.

Evidence from `~/.local/state/wt/reap.log` and `git worktree list`
(2026-10-12):

- 515 reap attempts, 64 reaped. 277 were refused with "branch has no
  upstream".
- Every clean `server` ephemeral fails with `fatal: working trees containing
  submodules cannot be moved or removed` (the `flexo` submodule), because
  removal never passes `--force`.
- Reaping only runs from the dashboard refresh (`cmd/wt/main.go:241`). Close a
  session and never open the dashboard, and nothing is ever collected. The
  `SessionEnd` hook documented in the README is not installed.
- `upc-web` alone has 35 worktrees: 15 under `.worktrees/` (wt ephemerals),
  16 under `.claude/worktrees/`, and 4 others (primary and siblings).

## What this adds

1. A precise definition of when an ephemeral worktree is **done** (§2),
   which keeps authoring worktrees alive and collects review worktrees fast.
2. **Triggers** that run the reap without the dashboard: on every `wt`
   command, on Claude Code `SessionEnd`, and on an optional hourly timer (§3).
3. **Removal that works** on worktrees with submodules, without weakening
   any safety check (§4).
4. **`wt <PR>`**: open a pull request by number or URL, including PRs from
   forks (§5).

## Principles

- **Never switch branches in the primary checkout.** A branch is opened in a
  worktree: the one that already has it checked out, or a new ephemeral one.
  wt already behaves this way; this spec makes it a rule every new code path
  must keep. (Motivating case, 2026-10-12: T3 Code's "local checkout" mode ran
  `git checkout main` in the `upc-web` primary, which had uncommitted edits,
  and failed.)
- **Removing a worktree never loses committed work.** The branch outlives the
  worktree (one narrow exception, §4.2). Only uncommitted and untracked files
  can be lost, and those are what the safety predicate guards.
- **Every destructive decision is re-checked at the moment of removal**, not
  when a sweep starts.
- **Permanent worktrees (`wt new`) are never collected automatically.**

## Verified facts

Confirmed on 2026-10-12, not assumed:

- `.wt-ephemeral` (v1) records `repo`, `branch`, `primary`, `created_at`, and
  nothing about the starting commit (`internal/ephemeral/marker.go`).
- `Workspace.LastUsed` is derived only from session transcript mtimes
  (`internal/aggregate/aggregate.go:191`).
- `trash.SoftDelete` runs `git worktree remove` without `--force` and keeps
  the branch (`internal/trash/delete.go`).
- `gh pr view N --json number,state,headRefName,headRefOid,isCrossRepository,headRepositoryOwner,url`
  returns all listed fields; `git ls-remote origin refs/pull/N/head` resolves
  on GitHub.
- git's documented behaviour: a worktree with initialized submodules can only
  be removed with `--force`; a **locked** worktree needs `--force` twice. One
  `--force` therefore never overrides a lock.

## 1. Scope

In: ephemeral worktrees created by `wt <branch>` / `wt <PR>` (marker present,
inside the repo's ephemeral directory).

Hook point only: worktrees from other creators (T3 Code, Claude Code, herdr).
The predicate and removal path are written so a creator adapter can opt a
worktree in later (`reap.creators`, sub-project 3). No creator is opted in by
this spec.

Out: JSON output for every command (sub-project 2), creator adapters (3),
jj workspaces (4), non-GitHub PR hosts.

## 2. When a worktree is done

### 2.1 Safety predicate (all must hold, always)

- Registered in `git worktree list`, not the primary, not locked.
- Valid marker (v1 or v2) and contained in the ephemeral directory (unchanged).
- `git status --porcelain --ignore-submodules=none` is empty: no tracked
  changes and no untracked files. Submodules are checked recursively with the
  same command.
- No live Claude/T3 session and no running process whose cwd is inside the
  worktree (existing checks).
- The checked-out branch equals the marker's branch (unchanged).

**Removed:** "has an upstream" and "0 ahead". The branch is kept, so neither
protects anything.

### 2.2 Done rule

Let **own commits** be the commits in `start..HEAD` whose author email equals
`git config user.email` in that repo.

- **No own commits** → done as soon as the safety predicate holds (review).
- **Own commits** → done when the safety predicate holds **and** either the
  branch's PR is MERGED or CLOSED, or the worktree has been idle for
  `idle_days` (whichever comes first).

`start` comes from the v2 marker (§2.4). For a v1 marker, own commits are
`git rev-list HEAD --not --remotes --author=<email>` — commits you authored
that exist on no remote.

### 2.3 Idle

`last_used` is the latest of:

- the newest session transcript mtime (today's source);
- the mtime of the worktree's git index and of its `HEAD` reflog;
- the last time `wt` opened the worktree (cd hand-off or resume), recorded in
  the v2 marker's `last_opened_at`.

A live session or running process means "in use now", regardless of
timestamps. `idle_days` defaults to 3, configurable globally and per repo.

### 2.4 Marker v2

```json
{"version": 2, "repo": "...", "branch": "...", "primary": "...",
 "created_at": "...", "start": "<sha>", "pr": 1234,
 "branch_created_by_wt": true, "last_opened_at": "..."}
```

`pr` and `branch_created_by_wt` are optional. v1 markers stay readable;
`ReadMarker` accepts both versions and never rewrites a v1 marker in place
except to add `last_opened_at`, which upgrades it to v2 with `start` absent
(meaning "use the v1 fallback").

### 2.5 Unpushed work stays visible

The dashboard shows a count line: "N local branches with unpushed commits and
no worktree", listing them on demand. This replaces the upstream gate as the
way unpushed work is kept from being forgotten.

## 3. Triggers

### 3.1 Commands

- `wt reap <path>` — evaluate one worktree.
- `wt reap --all` — sweep every configured repo.
- `wt reap --all --dry-run [--json]` — print the decision and reason per
  worktree; delete nothing.
- `wt reap` with no argument keeps reading Claude's `SessionEnd` JSON from
  stdin (unchanged).

### 3.2 Event triggers

- **Any `wt` command** spawns a detached `wt reap --all` at most once per
  `reap_trigger_interval` (default `10m`; timestamp file in
  `~/.local/state/wt`). The foreground command never waits for it.
- **Dashboard refresh** keeps its sweep, through the same code path.
- **Claude Code `SessionEnd`** runs `wt reap` for that worktree.
  `wt hooks install claude` prints the settings snippet and merges it into
  `~/.claude/settings.json` only after confirmation (`--yes` for scripts).
- T3 Code has no session-end event; its worktrees rely on the sweep and timer.

### 3.3 Timer

`wt timer install | uninstall | status` manages an hourly `wt reap --all`:
a launchd agent (`~/Library/LaunchAgents/…ephemera.reap.plist`,
`StartInterval 3600`) on macOS, a systemd user timer on Linux. It is the only
trigger that guarantees `idle_days` fires for a worktree never touched again.
It is optional; without it, behaviour degrades to the event triggers.

### 3.4 Concurrency

- A sweep takes an exclusive lock file in `~/.local/state/wt`. A second sweep
  that cannot take it exits 0 immediately.
- The safety predicate is re-evaluated per worktree immediately before
  removal. A session opened mid-sweep blocks removal.
- Every decision is appended to `reap.log` with its reason (new column; the
  existing three columns are unchanged).

## 4. Removal

### 4.1 Worktree

After the safety predicate passes:

- no initialized submodules → `git worktree remove <path>` (unchanged);
- initialized submodules → `git worktree remove --force <path>`.

One `--force` lets git delete what the predicate already proved disposable
(ignored files such as `node_modules`, build output) and never overrides a
lock. Locked worktrees are excluded by the predicate anyway.

The existing trash manifest records the removal, as today.

### 4.2 Branch

Kept, with one exception: if `branch_created_by_wt` is true and
`git rev-list <branch> --not --remotes` is empty, the local branch is deleted
too. This uses `git branch -D`, because `-d` refuses a branch not merged into
the current HEAD (always true for `pr/<N>`); the `rev-list` check is the
safety gate instead. It can be fetched again; this stops `pr/<N>` branches
from piling up.

### 4.3 Restore

`wt restore` (dashboard trash) recreates the worktree from its branch and
re-bootstraps. If the branch was deleted under §4.2, the trash entry carries
`pr`, and restore fetches `refs/pull/<pr>/head` again.

## 5. `wt <PR>`

### 5.1 Input

- `wt 1234`, `wt '#1234'`, `wt https://github.com/<owner>/<repo>/pull/1234`.
- An argument matching `^#?[0-9]+$` is always a PR number, never a branch.
- Repo resolution: a URL selects the configured repo whose `origin` matches
  `<owner>/<repo>`. A bare number uses the repo containing the cwd. Outside
  any configured repo: a picker on a TTY; otherwise exit 2 listing the
  configured repos.

### 5.2 Resolution

`gh pr view <n> --json number,state,headRefName,headRefOid,isCrossRepository,headRepositoryOwner,url`.
Missing `gh` or an unknown PR is an error with an actionable message.

- `isCrossRepository == false` → the existing `wt <branch>` path with
  `headRefName`.
- `isCrossRepository == true` → `git fetch origin refs/pull/<n>/head:pr/<n>`,
  then create an ephemeral worktree on `pr/<n>` with `pr` and
  `branch_created_by_wt: true` in the marker. Review only; pushing to the fork
  is out of scope.

### 5.3 Re-running

If a worktree for the PR already exists, `wt <PR>` goes there. When it has no
own commits and the safety predicate's cleanliness checks pass, it first
fast-forwards to the PR's current head (`headRefOid`). Otherwise it leaves the
worktree untouched and prints "PR has N new commits".

## 6. Configuration

New keys, all optional:

| Key | Scope | Default |
|---|---|---|
| `idle_days` | global, `[repo.X]` | `3` |
| `reap_trigger_interval` | global | `"10m"` |

Existing configs need no change.

## 7. Testing

All git behaviour is tested against real repositories through
`internal/gittest`; nothing mocks git.

- **Done rule**: a pure function from observed facts to `(decision, reason)`,
  table-tested across own commits × PR state × idle × each safety clause
  failing alone; v1 and v2 markers.
- **Removal**: a fixture repo with a submodule — a clean worktree is removed
  via the `--force` path; one with an uncommitted change inside the submodule
  is refused; a locked worktree is skipped; the branch is kept unless
  `branch_created_by_wt` and fully on a remote.
- **`wt <PR>`**: a fake `gh` on `PATH` returning canned JSON for same-repo,
  fork, closed, unknown PR, and missing `gh`. The fork path fetches from a
  local "origin" that carries `refs/pull/N/head`.
- **Concurrency**: two simultaneous sweeps — one runs, one exits on the lock;
  a worktree that becomes busy between planning and removal survives.
- **Timer**: only the generated plist / unit files are asserted; tests never
  load them.
- **Regression**: the two dominant `reap.log` failures (no upstream; submodule
  refusal) are fixtures that must now reap.

## 8. Success criteria

- A review worktree (`wt <branch>` or `wt <PR>`, no own commits) is gone
  within one sweep of its session ending, with the `SessionEnd` hook
  installed.
- Server ephemerals with `flexo` reap.
- Running `wt reap --all --dry-run` on the 2026-10-12 `upc-web` checkout
  explains every one of its 15 `.worktrees/` entries with a concrete reason.
- No worktree with uncommitted or untracked changes, a live session, a
  running process, or a lock is ever removed.

# `wt` — Multi-Repo Worktree & Session Dashboard

**Date:** 2026-08-13
**Status:** Approved design, not yet implemented
**Language:** Go + bubbletea

## Problem

Work is spread across 40+ git worktrees of a `server` repo living under four
different roots, plus further worktrees of `dashboard` and `web`. Three
distinct costs fall out of this:

1. **Finding a free workspace.** Locating a worktree whose PR has merged means
   `cd`-ing through candidates one at a time.
2. **Finding a Claude session.** A session started days ago in one of those
   worktrees is effectively unfindable; nothing lists sessions by location.
3. **Bootstrapping a new worktree.** A fresh worktree lacks the gitignored
   `.env*` files and an initialised submodule, so it cannot boot the app
   until those are restored by hand.

The four roots, as of writing:

| Root | Count | Created by |
| --- | --- | --- |
| `code/server-*` siblings | 8 | the user, by hand |
| `server/.worktrees/` | 22 | scripts and agents |
| `server/.claude/worktrees/` | 5 | Claude Code's `EnterWorktree` |
| `~/.sprout/worktrees/server/` | 8 | another worktree-per-branch agent tool |

## Constraints

These are requirements, not preferences, and the design is bound by them.

- **Flat, `cd`-able paths.** New worktrees are created as siblings
  (`code/server-<slug>`), matching existing muscle memory. Deeply nested
  paths are acceptable only for worktrees other tools create.
- **Nothing is ever auto-deleted.** A merged PR does not mean a workspace is
  finished; long-running support sessions deliberately outlive their branch.
  Cleanup is always explicit and confirmed.
- **Multi-repo from day one.** `repo` is a first-class field. Bootstrap rules
  are per-repo config, so adding `dashboard` later is a config entry rather
  than a rewrite.

## Prior art considered

- **worktrunk** — strong at creation (`.worktreeinclude` copies gitignored
  files, post-create hooks). Single-repo, CLI not TUI, and cannot see worktrees
  Claude creates under `.claude/worktrees/`. Its file-copy idea is adopted; the
  dependency is not.
- **claude-squad** — a TUI for parallel agents, but it *spawns and owns*
  sessions inside **tmux**. tmux is not installed here, and it cannot discover
  the 40+ pre-existing worktrees or the sessions already started from ordinary
  terminal tabs. Wrong shape.

Nothing found discovers *pre-existing* worktrees across *multiple roots and
repos* and maps Claude sessions and running processes onto them. That gap is
what `wt` fills.

## Verified mechanisms

Each of these was confirmed empirically before being designed against.

- **`git worktree list --porcelain` already returns all four roots** from any
  single checkout, including a foreign tool's worktree directory and
  `.claude/worktrees`. Discovery needs no path guessing or directory scanning
  for worktrees.
- **Transcripts carry their own labels.** Session `.jsonl` files contain
  `ai-title` (a generated human-readable title), `last-prompt`, and `pr-link`
  (PR number, URL, repository) records, alongside `user` records bearing `cwd`,
  `gitBranch`, and `timestamp`. No summarisation work is required.
- **Liveness is detectable.** Claude processes matching `/versions/<N>` and
  carrying `--session-id` resolve to a real working directory via
  `lsof -a -p <pid> -d cwd -Fn`. Pre-warmed `--bg-spare` processes have a cwd
  under `/tmp` and must be filtered out.
- **`WorktreeCreate` and `WorktreeRemove` are real hook events** in the
  installed Claude Code (2.1.229), confirmed present in the binary alongside
  `PreToolUse` and `SessionStart`.

## Data model

```toml
# ~/.config/wt/config.toml
scan_roots = ["~/code"]

[repo.server]
env         = [".env", ".env.dev", ".env.prod", ".env.test"]
submodules  = ["corelib"]
post_create = ["deno install"]        # optional

[repo.dashboard]
env = [".env", ".env.local", ".env.tunnel"]

[repo.web]
env = [".env", ".env.local"]
```

Env file lists above are the ones actually present in each repo, confirmed on
disk. `.env.example` and `.env.playwright.example` are tracked templates and are
deliberately excluded — git already provides them in every worktree.

**Workspace**

| Field | Meaning |
| --- | --- |
| `repo`, `path`, `branch`, `head` | identity |
| `dirtyCount`, `ahead`, `behind` | git state |
| `pr{number, state}` | resolved by branch, or via a session's `pr-link` |
| `sessions[]`, `procs[]` | attributed by path prefix |
| `lastUsed` | most recent of session mtime and git activity |
| `kind` | `primary` \| `sibling` \| `nested` \| `claude-managed` \| `foreign` |

`kind` is what surfaces that most of these worktrees were created by an agent
rather than deliberately.

**Session**

`{id, title, lastPrompt, prNumber, branch, cwd, mtime, live, pid}`

## Discovery

Four collectors run concurrently. Each degrades to an empty result rather than
failing the render, so a missing `gh` or a denied `lsof` never blanks the UI.

| Collector | Mechanism | Cost |
| --- | --- | --- |
| Worktrees | `git worktree list --porcelain` per repo | ~5ms, one call per repo |
| Git status | `git status --porcelain` + `rev-list` per worktree | parallel across worktrees |
| Sessions | `~/.claude/projects/<flat-cwd>/*.jsonl` | tail-read only |
| Live / procs | one `ps`, then `lsof -d cwd` per candidate pid | ~50ms total |

Three constraints govern the implementation:

**Path flattening is lossy and must only be applied forward.** The project
directory for a worktree is its absolute path with both `/` and `.` replaced by
`-`, so `…/server/.worktrees/foo` becomes `-Users-…-server--worktrees-foo`. The
mapping is not reversible. `wt` flattens each *known* worktree path and looks up
that directory; it must never parse a project directory name back into a path.

**Transcripts must be tail-read.** The largest local transcript seen was tens of
megabytes across thousands of records. Sessions are read backwards for the last
`ai-title`, `last-prompt`, and `pr-link` record rather than parsed from the top.

**PR state costs one call per repo, not per worktree.**
`gh pr list --repo <r> --state all --json number,headRefName,state` returns
everything; worktrees match locally by branch name. That is a handful of
network calls rather than one per worktree. Results are cached on disk with a
TTL and refreshed in the background, so the UI paints immediately from cache
and the PR column fills in after. Sessions' `pr-link` records resolve worktrees
whose branch was renamed.

## TUI

Panes are cross-linked: selecting a worktree filters the sessions and processes
panes to that worktree. `Tab` moves focus.

```
┌ WORKTREES ────────────────────────┬ SESSIONS (server-cache) ───────────────┐
│ ▾ server (43)                     │ ● Rework the retry backoff             │
│   ● server              #4003  3✎ │   "make the retry delay exponential…"  │
│   ● server-cache          #4001   │   PR #4002 · 3m ago · LIVE pid 49745   │
│   ○ server-prs          MERGED    │ ○ Invite code split                    │
│   ○ server-risk         develop   │   "check the parent-code casing"       │
│   ○ server-master       12✎       │   PR #4002 MERGED · 4d ago             │
│   ⚠ .worktrees/queue-lock  agent  │                                        │
│   ⚠ .sprout/sprout-11ab65f9 agent ├ PROCESSES ─────────────────────────────┤
│   … 36 more                       │ deno rerun-sync.ts --env-file=.env.…   │
│ ▾ dashboard (6)                 │   3m · pid 93927 · server-cache        │
│ ▾ web (2)                         │                                        │
└───────────────────────────────────┴────────────────────────────────────────┘
 j/k move  ⏎ cd  n new  r resume  p prune  / filter  tab pane  R refresh
```

### `⏎ cd`

The TUI writes the chosen path to `$TMPDIR/wt-cd` on exit; a thin `wt()` zsh
function reads that file and `cd`s. No process can change its parent's working
directory, so the wrapper is required.

### `r resume`

For an **idle** session, run `claude --resume <id>` in that worktree.

For a **live** session already running in another terminal tab, `wt` cannot
attach — without tmux there is no way to reattach a foreign terminal's PTY. It
shows the pid and path and offers to open a new terminal window there. True
"jump to the running session" would require adopting tmux; that is explicitly
out of scope.

### "Free" is advisory

A hint is computed from four signals — PR merged, working tree clean, no live
session, no running process — but it only ever sorts and colours rows. Deletion
happens solely via `p`, requires confirmation, and hard-refuses any worktree
that is dirty, has a live session, or has a running process.

## Create and bootstrap

`n new <branch>`:

1. `git worktree add ~/code/<repo>-<slug> <branch>` — a flat,
   `cd`-able sibling. The slug is the branch name with its leading
   `feature/`, `fix/`, `hotfix/`, `perf/`, `ci/`, `test/`, `docs/`, `chore/` or
   `refactor/` prefix stripped and remaining `/` replaced by `-`, so
   `fix/rate-calc-nan-guard` yields `server-rate-calc-nan-guard`. An explicit
   name may be passed to override it. If the resulting path already exists,
   `wt` stops and reports rather than picking a variant name.
2. **Symlink** the configured env files back to the primary checkout, so a
   rotated credential propagates rather than going stale across many copies. A
   per-file `copy = [...]` override exists for files a branch must diverge on.
3. `git submodule update --init --recursive <submodule>`, **pinned to the SHA
   the branch records**, not the submodule's tip. An unpinned submodule diff is
   a known cause of silent typecheck failure, so bootstrap asserts the
   submodule is clean afterwards and reports when it is not.
4. Run any configured `post_create` commands.

### Hook wiring

The same code path is exposed as `wt bootstrap <path>` and registered as a
**`WorktreeCreate` hook**. When Claude creates a worktree under
`.claude/worktrees/`, it receives the same env files and submodule as a
hand-made one, so it stops being a second-class checkout that cannot boot the
app. `WorktreeRemove` drops the entry from the cache.

This closes the original problem at its root: the reason worktrees had to be
hand-made siblings was that agent-made ones were unusable.

## Explicitly out of scope

- **No daemon.** A cold scan is ~50ms; a background service is unjustified.
- **No config UI.** The TOML is ten lines.
- **No tmux adoption.** It would enable attaching to live sessions, but it is a
  separate decision with its own cost.
- **No auto-pruning**, under any heuristic. See Constraints.

## Success criteria

1. Locating a free workspace takes one command and no `cd`-ing.
2. A Claude session started in any worktree is findable by its title or last
   prompt, with its location and live/idle state shown.
3. A newly created worktree — by hand *or by Claude* — can boot the app
   without manual setup.
4. Cold start to painted UI is under 200ms with PR state served from cache.

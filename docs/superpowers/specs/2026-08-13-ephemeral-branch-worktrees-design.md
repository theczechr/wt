# `wt <branch>` — cross-repo branch lookup with ephemeral worktrees

Status: approved design, not yet implemented
Date: 2026-08-13

## Problem

Reviewing a pull request starts by copying a branch name off GitHub. What
follows is manual: work out which repo the branch belongs to, hunt for a
long-lived worktree that is free, check the branch out there, and hope the
env files are in place. When the review ends, that worktree stays occupied.

`wt` already knows how to create and bootstrap a worktree (`wt new <repo>
<branch>`), but it requires the repo name up front and produces a permanent
flat sibling. Neither fits a review, which is short-lived and starts from a
branch name alone.

## What this adds

`wt <branch>` resolves a branch name across the configured repos, and either
cds into a worktree that already has it checked out, or creates a short-lived
one — bootstrapped from the repo's existing config — that is collected
automatically once the session ends and nothing would be lost.

## Design principle change

The README states: *"wt never deletes anything. There is no prune
subcommand, and none should be added."*

This design narrows that rule rather than discarding it. wt may delete
**only** a worktree it created itself, marked as ephemeral, sitting inside the
configured ephemeral directory, and passing every clause of the reap
predicate in §5. Worktrees you created — by hand or via `wt new` — remain
untouchable, and there is still no general prune. The README must be updated
to state the narrowed rule; leaving the absolute claim in place would be
false.

## Verified facts

Behaviour confirmed by probe on 2026-08-13, not assumed. Each of these
changed the design.

1. **`SessionEnd` payload.** JSON on stdin: `session_id`, `prompt_id`,
   `transcript_path`, `cwd`, `hook_event_name`, `reason`. `reason` is one of
   `clear`, `resume`, `logout`, `prompt_input_exit`,
   `bypass_permissions_disabled`, `other`. The hook cannot block; exit codes
   are ignored; stderr reaches the transcript only. The docs do not promise it
   runs on crash or SIGKILL.

   `clear` and `resume` fire while the user is still working in that
   directory. Reaping on them would delete a worktree out from under a live
   session.

   **Correction, added after review:** this spec never asked whether the
   *ending* session is still running when its own hook fires. It is. Claude
   forks the hook command and waits for it, so `ps` reports the ending
   session for the whole life of `wt reap`, and its transcript exists by
   definition — the session has been used. Confirmed by `ps` during a real
   `SessionEnd` on 2026-08-13. Taken together with clause 6 below, that made
   the predicate refuse *every* hook-driven reap: fail-closed, so nothing was
   ever at risk, but the headline trigger would never once have fired. `wt
   reap` therefore passes the payload's `session_id` down to the predicate,
   which exempts that one id and no other.

2. **`Ahead == 0` does not mean "nothing unpushed."**
   `discover.ParseStatusV2` documents that the `# branch.ab` header is absent
   entirely when a branch has no upstream, yielding `ahead=0, behind=0` —
   identical to a fully-pushed branch. A branch with no upstream is the
   *most* unpushed state possible. Upstream existence must be checked
   separately.

3. **`git worktree remove` without `--force` succeeds** when only ignored
   files are present (`node_modules`, a symlinked `.env`). It can therefore
   serve as a final safety gate without being forced past.

4. **The marker file is visible to `git status`** as `?? .wt-ephemeral`
   unless excluded. Without an exclude entry, no ephemeral would ever be
   clean enough to reap.

5. **`.worktrees/` is not gitignored** in `server`, `dashboard`, or `web`,
   and no global `core.excludesfile` is set.

6. **`git status` can fail for unrelated reasons.** At least one worktree in
   this checkout fails it outright because of a submodule misconfiguration —
   a real, observed failure, not a hypothetical. Empty output from a failed
   command must never be read as "clean".

7. **`trash.SoftDelete`'s `PruneBlockers` gate is weaker than this design's.**
   Read from `internal/trash/delete.go` on 2026-08-13: it refuses on
   `model.PruneBlockers()` — primary checkout, `DirtyCount > 0`, live session,
   running processes. It has no upstream or ahead check, so it cannot by
   itself decide an unattended deletion, which is why `ephemeral.Blockers`
   leads.

   Two corrections to an earlier, overstated version of this note:

   - **`SoftDelete` was never the only gate.** It runs `git worktree remove`
     without `--force`, and git independently refuses a worktree containing
     modified or untracked files. So even with `PruneBlockers` fooled, work
     could not have been silently lost. (This is consistent with verified
     fact 3: *ignored* files — `node_modules`, a symlinked `.env` — do not
     block removal, while modified or untracked ones do.) `--force` and
     `git branch -D` appear nowhere in this codebase's executable code, only
     in comments explaining their deliberate absence. Anything added here
     must preserve that.
   - **The `DirtyCount` ambiguity has since been fixed upstream**, on
     `feat/wt-implementation`: `model.Workspace` gained `StatusKnown bool`,
     set true only on `FillStatus`'s success path, and `PruneBlockers` now
     reports `"git status could not be read"` when it is false. The polarity
     is deliberate — the zero value is the *blocked* state, so a bare
     `Workspace{}` literal can never read as "known clean".

   **Merge obligation:** that field does not exist on this branch yet. When
   the branches merge, `inspect` must set `StatusKnown: true` on the success
   path of its own status read, or `SoftDelete` will refuse every reap. The
   failure mode is fail-closed (nothing gets reaped) rather than dangerous,
   but the feature stops working until it is done.

### A note on provenance

Points 7 and the trash integration below arrived after this spec was first
approved: a separate workstream added `internal/trash` (soft delete, restore,
expiry) to the same branch while this plan was being executed. The decision to
route reap through it was taken deliberately — see "Deletion goes through the
trash system" — and this document was revised rather than left stale.

## 1. Command surface

`main.go`'s switch gains a default branch: an argument that is not a reserved
word is a branch name.

Reserved: `status`, `new`, `bootstrap`, `reap`, `open`, `help`, `-h`,
`--help`. `wt open <branch>` is the explicit spelling, so a branch genuinely
named `new` stays reachable. The not-found message lists the reserved words,
so a typo (`wt statuz`) is legible rather than mysterious.

| Command | Effect |
| --- | --- |
| `wt <branch>` | resolve, then cd to an existing worktree or create an ephemeral one |
| `wt open <branch>` | identical; unambiguous when the branch name collides with a subcommand |
| `wt reap [<path>]` | evaluate one worktree for collection; with no argument, read `SessionEnd` JSON from stdin |

## 2. Resolution

1. For each repo with a `[repo.*]` block, resolve the primary via the
   existing `primaryPath`. Repos are searched regardless of the current
   directory: `wt` behaves identically from `/code` and from
   `/code/wt`.
2. Per repo, one `git for-each-ref` matching `refs/heads/<branch>` and
   `refs/remotes/*/<branch>` exactly. Concurrent across repos, bounded by
   `runtime.NumCPU()`, matching `aggregate`'s existing fan-out.
3. Zero matches anywhere: `git fetch --prune` across the configured repos
   concurrently, then one re-search. Still zero — exit 1, naming the repos
   searched and stating that a fetch was attempted.
4. **Already checked out**: if the branch is checked out in any worktree of
   the matched repo, cd there and create nothing. Git refuses one branch in
   two worktrees, so this must be handled; it also happens to solve the
   "find a free worktree" problem directly. Such worktrees are never marked
   ephemeral and never reaped.
5. Exactly one repo matches: use it. If that repo has `ephemeral = false`
   and the branch is not already checked out, wt creates nothing and exits 1
   pointing at `wt new <repo> <branch>`. Such repos are still *searched* —
   step 4 remains useful for them.
6. More than one: a small bubbletea chooser listing `repo — local | <remote>
   — last commit date`, with `j`/`k`/`⏎` and Esc to cancel, reusing
   `styles.go` and `renderPane`. When stdout is not a TTY it does not
   prompt — exit 1, listing the matches and pointing at `wt new <repo>
   <branch>`.

### Remote-only branches

`bootstrap.Create` currently does `if branchExists → add <branch>, else →
add -b <branch>`, which branches off the primary's current `HEAD`. For a
remote-only PR branch — the primary case here — that silently produces an
**empty** branch instead of the PR's code.

The new path resolves explicitly:

- local ref exists → `git worktree add <path> <branch>`
- remote-only → `git worktree add -b <branch> <path> <remote>/<branch>`,
  creating it tracking the remote

`<remote>` is the repo's `default_remote` (default `origin`).

## 3. Creation and layout

Path: `<primary>/<ephemeral_dir>/<slug>`, `ephemeral_dir` defaulting to
`.worktrees`, `slug` from the existing `bootstrap.Slug`. If that path exists
but is not the expected worktree, wt refuses rather than inventing a variant
name — the "refuse rather than invent" principle already stated in `Create`.

**Excludes.** wt appends `.worktrees/` (or the configured `ephemeral_dir`)
and `.wt-ephemeral` to the repo's `info/exclude`, located via `git rev-parse
--git-common-dir` so it resolves correctly from either a primary or a
worktree. That file is local-only and never committed. Writes are idempotent:
a line is added only if absent. This is the only place the design mutates an
existing repo's state.

The `.wt-ephemeral` entry is load-bearing, not tidiness — see verified fact
4. The reap predicate additionally ignores that path explicitly, so it stays
correct even if the exclude write failed.

**Bootstrap.** After `git worktree add`, `bootstrap.Run` is called unchanged:
env symlinks, submodules, post-create hooks. No new setup logic.

## 4. Configuration

Settings stay in `~/.config/wt/config.toml`, in the existing `[repo.*]`
blocks. New keys, all optional:

| Key | Default | Purpose |
| --- | --- | --- |
| `ephemeral_dir` | `.worktrees` | Location of ephemerals, relative to the primary |
| `ephemeral_post_create` | falls back to `post_create` | Lighter setup for review worktrees |
| `default_remote` | `origin` | Remote used to resolve remote-only branches |
| `ephemeral` | `true` | Set false to opt a repo out of this mode |

`ephemeral_post_create` is a separate key rather than a boolean skip flag
because "skip setup" and "run different setup" are distinct needs and only
the latter composes. Example: `server` initialises the `corelib` submodule via
`post_create`; paying that on every glanced-at PR is not wanted, but a
cheaper subset may be.

## 5. Reap lifecycle

### Marker

`.wt-ephemeral` in the worktree root, JSON: `version`, `repo`, `branch`,
`primary`, `created_at`. Written only after `bootstrap.Run` succeeds — a
worktree whose setup failed is left in place for inspection, not silently
collected. Deleting the marker by hand promotes the worktree to permanent.

### Trigger

A `SessionEnd` hook invokes `wt reap`, which reads the JSON from stdin, takes
`cwd`, and resolves upward to the containing worktree root — `cwd` is
Claude's working directory and may be a subdirectory.

**Reason handling is a deny-list**: reap on every reason except `clear` and
`resume`. An allow-list is wrong here — a normal quit most plausibly arrives
as the catch-all `other`, so an allow-list would make the feature silently
never fire. The deny-list covers the two reasons known to fire mid-work, and
the live-session clause below independently catches any reason misjudged
here.

### Predicate

Every clause must hold. Any failure leaves the worktree alone.

1. `.wt-ephemeral` present and parseable
2. path contained within that repo's configured `ephemeral_dir` — an
   independent second gate, so a bug in marker handling alone cannot
   authorise a deletion
3. registered as a worktree of that primary, and not the primary itself
4. `git status --porcelain` reports clean **and the command succeeded**. A
   failed status is "not clean" (verified fact 6)
5. an upstream exists (`git rev-parse --abbrev-ref @{u}` succeeds) **and**
   `Ahead == 0`. Both clauses, for the reason in verified fact 2
6. no live Claude session and no running processes in the worktree, via
   `discover`, minus the one session id a `SessionEnd` hook names as its own

   Two limits on this clause, both found on review and both now true of the
   code rather than of the prose:

   - **A session is detected two independent ways.** Transcript discovery
     (`discover.SessionsFor`) alone was not enough: it keys on
     `~/.claude/projects/<flattened cwd>`, so a session launched from a
     subdirectory keys elsewhere, a session that has not flushed its first
     message has no directory at all, and an unreadable projects dir returns
     `nil`. All three read as "no session" and would have authorised a
     deletion — the same failed-probe-means-safe trap as verified fact 6. The
     second detector is the process table: `discover.SnapshotErr` already
     resolves every Claude process's cwd, so a session process at or under
     the worktree blocks on its own.
   - **"No running processes" means no *recognised* processes.** `discover`
     resolves cwds only for commands passing `interesting()` (`deno`, `node`,
     `supabase`, `psql`, `docker`, `go run`, `vite`, `next`). A `cargo build`
     or a plain shell in the worktree does not block. Widening that list was
     considered and rejected: it is a shared, perf-sensitive path the
     dashboard also uses. Their output (`target/`, `.pytest_cache/`) is
     conventionally gitignored, so it does *not* dirty the tree and does not
     block via clause 4 either — a reap in that state removes the worktree
     and the output with it, recoverable only as a worktree restore, not as
     the build artifacts. Stated plainly in the README rather than papered
     over.

### Deletion goes through the trash system

Removal is `trash.SoftDelete(ctx, ws, primary)`, not a direct `git worktree
remove`. That function removes the checkout with a non-`--force` `git worktree
remove` and records a manifest `Entry`, so a mistaken reap stays recoverable
through the dashboard's trash view.

Three consequences follow, each load-bearing:

1. **`ephemeral.Blockers` remains the authoritative gate.** `SoftDelete` gates
   only on `model.PruneBlockers()`, which is strictly weaker than the
   predicate above: it has no upstream/ahead check at all, and its
   `DirtyCount` comes from `discover.FillStatus`, which swallows errors and
   leaves the count at 0 when `git status` fails — verified fact 6, the exact
   trap this design exists to avoid. `Blockers` therefore runs first and
   decides; `SoftDelete`'s own check becomes the second, independent layer, in
   the doubled-guard style used elsewhere in this codebase.

2. **The `model.Workspace` handed to `SoftDelete` must be genuinely
   populated** — `Path`, `Branch`, `Repo`, `Kind`, `DirtyCount`, `Sessions`,
   `Procs` — from state `Blockers` has already gathered. A zero-valued
   Workspace would make `PruneBlockers` return nothing, reducing that second
   layer to theatre.

3. **Reap always soft-deletes, whatever `delete_mode` says.** The top-level
   `delete_mode = "hard"` governs the dashboard's manual delete, where the
   user is present and confirming. A reap is unattended: nobody is watching to
   catch a mistake, so it must stay recoverable. A global preference for hard
   deletion must not silently make an automatic deletion irreversible.

### What reap does not touch

The branch ref always survives. This is now a requirement rather than a
preference: `trash.Restore` rebuilds a worktree with `git worktree add <path>
<branch>` and never `-b`, so deleting the branch would leave a manifest entry
that cannot be restored. The `ephemeral_delete_branch` config key is therefore
dropped — it cannot coexist with soft deletion.

**A restored ephemeral becomes permanent.** `trash.Restore` re-creates the
directory and re-runs `bootstrap.Run`, but knows nothing about
`.wt-ephemeral`, which was deleted along with the worktree. The restored
worktree therefore carries no marker, `Blockers` refuses it, and it is never
reaped again. That is the right outcome: restoring one by hand is a deliberate
act that promotes it to permanent.

**Known gap:** `git stash` writes to `refs/stash` in the common dir, so a
stash created inside an ephemeral survives its deletion. The work is
recoverable but the situation is confusing. Documented in the README; no code
handles it.

### Sweep

Because `SessionEnd` is not guaranteed on crash, reboot, or SIGKILL
(verified fact 1), `wt` also sweeps on launch: scan each configured
`ephemeral_dir` for markers and apply the same predicate. Given `PERF.md`'s
focus on time-to-first-frame, this runs inside the existing background
refresh goroutine, never on the paint path.

### Visibility

`SessionEnd` stderr reaches only the transcript, so a blocked reap would
otherwise be invisible. Two remedies: `wt reap` appends a line to
`~/.local/state/wt/reap.log`, and a blocked ephemeral simply remains a
worktree, so it appears in the dashboard.

## 6. Testing

A new `internal/gittest` helper stands up origin + clone + worktree in a temp
dir, used only by tests that need real git semantics. Existing tests use
`t.TempDir()` with pure functions; that stays the default.

**Pure, table-driven:** arg parsing across every reserved word and a branch
name, including `wt open new`; `SessionEnd` JSON parsing, asserting `clear`
and `resume` do not reap and `other` does; `info/exclude` write idempotency;
ephemeral path construction from `ephemeral_dir`; config defaults and
`ephemeral_post_create` falling back to `post_create`.

**Against real git, one test per predicate clause, each asserting refusal:**
dirty tree; untracked file; no upstream with local commits (verified fact 2 —
the branch must survive); `Ahead > 0`; `git status` failing; marker absent;
marker present but path outside `ephemeral_dir`; path is the primary. Plus
the positive case: clean, pushed, marker present, inside the directory —
asserting removal and that `git worktree list` no longer shows it.

**Resolution, against real git:** branch in one repo; branch in two repos
(asserting the chooser is reached, not how it renders); remote-only branch,
asserting the new worktree's `HEAD` matches `<remote>/<branch>` and not the
primary's HEAD — pinning the empty-branch bug from §2; branch already checked
out elsewhere, asserting nothing is created and the existing path is chosen.

The refusal tests carry the weight. Everything else is a feature; those stand
between a `SessionEnd` and uncommitted work.

## Out of scope

- A central ledger of live ephemerals. Considered and rejected: it is the one
  design where state can drift from disk, and a lost ledger orphans worktrees
  nothing will collect. The marker file keeps authority next to the thing
  being deleted.
- A general `wt prune` over hand-made worktrees.
- Auto-launching Claude from `wt <branch>`. `wt` continues to cd only.
- Per-repo `.wt.toml` files. Settings stay central.

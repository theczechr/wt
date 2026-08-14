# Startup performance

`wt` opens a dashboard the user hits constantly, and it took 1.9s to draw
anything. This document records what was slow, what changed, and what was
actually measured -- on this machine, against the real fleet (71 worktrees
across `server`, `web`, `dashboard`, three repos under one scan root).

## Before / after

All timings are `/usr/bin/time -p`, warm runs after one warm-up, same
machine (12 cores). "Cold" means `~/.cache/wt` deleted first.

| | before (`bd026aa`) | after | change |
| --- | --- | --- | --- |
| `wt status` cold | 3.0 -- 3.4s | 2.4 -- 2.8s | ~20% faster |
| `wt status` warm | 1.0 -- 1.3s | 0.66 -- 0.72s | ~40% faster |
| TUI: time to paint-ready data | *(N/A -- always blocked on a full Collect)* | **~0.77ms** (778µs avg, 5 runs) | now exists at all |
| TUI: `View()` render (71 workspaces, synthetic) | *(N/A)* | ~133µs/op | negligible |

The number that actually matters -- time to first painted frame -- is the
last two rows combined: on a snapshot hit, `wt` has real data ready to
render in under a millisecond, and rendering it costs another ~0.13ms. That
is the ~1.9s "horrendous" launch collapsed to something the terminal's own
paint latency dominates, not `wt`'s.

`wt status`'s output was diffed field-by-field (`DirtyCount`, `Ahead`,
`Behind`, path set) between the before and after binaries across all 71
real worktrees: **zero differences.** The speedup didn't come from
returning less data.

### Method notes

- Cold/warm `wt status` numbers above are from a clean before/after pair
  built in the same session (`git worktree add` of `bd026aa` next to HEAD,
  both built with the same toolchain, both run against the same 71 real
  worktrees back-to-back). Multiple runs are reported as a range because
  both binaries show real machine-load variance run to run; the ranges
  don't overlap, so the improvement is real, not noise.
- "Time to paint-ready data" is `main.go`'s bare-invocation path from
  process start to the point `ui.Run` is called, printed to stderr under
  `WT_DEBUG_TIMING=1`. It stops there because `tea.NewProgram(...).Run()`
  needs a real TTY, which isn't available for scripted measurement; the
  `View()` benchmark below fills in the next step.
- The `View()` number is `go test -bench=BenchmarkViewSeventyOneWorkspaces
  ./internal/ui` (`internal/ui/render_bench_test.go`) against a synthetic
  71-workspace, 3-repo fleet shaped like the real one.

## What each optimisation did

### 1. One git fork per worktree instead of two

`FillStatus` ran `git status --porcelain` and `git rev-list --left-right
--count @{u}...HEAD` separately -- 142 forks for 71 worktrees. Both pieces
of information are in `git status --porcelain=v2 --branch`'s output, so
`FillStatus` now runs one command instead of two. `--no-optional-locks` was
also added to every other read-only git invocation on the status/TUI path
(`worktree list`, `submodule status`, `show-ref`, `rev-parse
--git-common-dir`), which avoids `index.lock` contention when dozens of
these run concurrently against one repo.

**A real correctness bug turned up while measuring this, not while writing
it**: one worktree under `server` fails `git status` outright, because of a
submodule misconfiguration in that checkout -- a pre-existing, unrelated
breakage. The old two-call `FillStatus` still
reported its `Ahead`/`Behind` correctly, because `rev-list` doesn't touch
the index or the submodule tree and succeeded independently. The naive
single-call version failed atomically and silently zeroed both. Fixed by
falling back to the `rev-list`-only call when the combined one fails --
the common case (status succeeds) still costs exactly one fork; only a
worktree in this broken state pays for a second one. Covered by
`TestFillStatusFallsBackToRevListWhenStatusFails`, which reproduces the
same failure mode deterministically via a corrupted `.git/index`.

`ParseStatus` and `ParseAheadBehind` are untouched (still public, still
tested); `ParseStatusV2` is new, and `FillStatus` uses it exclusively.

### 2. Cache session labels by transcript (mtime, size)

274 sessions across 677 transcript files (1.8GB total) were tail-read and
JSON-parsed on *every* run, even though almost none of them change between
two runs seconds apart. `SessionCache` (`internal/discover/sessioncache.go`)
persists `path -> {Session, size}`, validated against the transcript's
current `(mtime, size)` on every lookup -- any mismatch falls straight
through to a live `ReadSessionTail`, same as a cold cache would. It's
loaded once per `Collect`, shared read-mostly across every per-worktree
goroutine, and flushed once at the end (not once per worktree), so the
disk I/O for the whole cache stays at one read + one write regardless of
worktree count. The write is atomic (temp file + rename); a truncated or
corrupt file on load degrades to an empty cache, never an error. `Flush`
prunes entries whose transcript no longer exists, so the file doesn't grow
forever as Claude Code's own retention deletes old transcripts.

### 3. Paint immediately from a snapshot, refresh in the background

This is the one the user actually feels. `main.go` used to block on a full
`aggregate.Collect` before `ui.Run` was even called, and `Init()` returned
`nil` -- the original spec called for painting from cache and filling in
real data after, but it was never built.

`Collect` now persists its result (`aggregate.WriteSnapshot`, atomic,
next to the PR and session caches) after every successful run, from either
`wt status` or the TUI. The bare `wt` invocation tries
`aggregate.LoadSnapshot` first: a hit paints immediately and `ui.Run` kicks
a real `Collect` off as a `tea.Cmd` from `Init()`, swapping the data in via
a `refreshedMsg` and keeping the cursor on the same workspace by **path**
(not index) so the selection doesn't jump when the refreshed set reorders,
gains, or drops entries. A miss (first run, or a cleared cache dir) falls
back to today's behaviour -- collect, then paint -- so an empty dashboard
is never shown. The `R` key triggers the same refresh on demand, gated
against stacking concurrent `Collect`s while one is already in flight. A
`⟳` in the help line marks a refresh in progress, so a stale,
snapshot-painted view is never mistaken for current data.

`wt status` is untouched: it still always calls `aggregate.Collect`
directly and never reads the snapshot -- it's a scripting interface and
must return live, complete data every time. Its own `Collect` call now
also refreshes the snapshot as a side effect, same as the TUI's does,
which is how the snapshot stays warm between interactive launches.

## What did NOT pay off

**Bounding the per-worktree fan-out to `runtime.NumCPU()`.** `Collect`'s
inner loop spawned one goroutine per worktree unboundedly (71 at once, 3
repos collecting concurrently, so potentially all 71 git processes forking
at the same instant). Bounded it with one shared semaphore across the
whole `Collect` call. Measured with 10 interleaved `wt status` runs each
(bounded / unbounded alternating, to cancel out machine-load drift over
the measurement window): wall-clock (~0.65 -- 0.77s either way) and total
CPU time (~6.0 -- 6.3s combined user+sys either way) were statistically
indistinguishable. This workload is fork/exec- and IO-bound, not
CPU-bound -- 71 concurrent git processes don't meaningfully contend for 12
cores here. **Kept anyway**: it is not a measured loss, and it caps
worst-case concurrent process count as worktree/repo counts grow, which a
71-worktree, 12-core measurement can't rule out mattering elsewhere (a
smaller machine, a much larger fleet, a stricter process ulimit). If a
future measurement shows it costing something, drop it.

## What still costs time, and the realistic floor

- **Cold-run cost is largely irreducible**: `wt status` cold is still 2.4
  -- 2.8s, because both caches (PR, session) start empty on a cold run and
  every optimisation above except OPT1 (the git-fork halving) is a cache
  that has nothing to hit yet. A cold run still pays for 71 git status
  calls, 3 `gh pr list` calls (one per repo, already batched -- see
  `PRsForRepo`'s doc comment), and a full tail-read of every transcript
  that isn't already cached. This is the honest floor for "no prior state
  helps at all."
- **`wt status` warm (0.66 -- 0.72s) is now dominated by things this
  branch didn't touch**: `Snapshot()`'s `ps` + up to ~16 concurrent `lsof`
  calls (`internal/discover/procs.go`), and `gh pr list`'s ~10-minute TTL
  cache expiring. Neither was in scope here.
- **The TUI's real number is no longer "however long a full Collect
  takes"** -- it's sub-millisecond to first frame, with the live refresh
  landing in the background a fraction of a second later, same as `wt
  status` warm. That's the actual fix for what the user asked for.

## Commits

- `d4d8879` -- fold `status`+`rev-list` into one `git status
  --porcelain=v2` call (OPT1)
- `d48a222` -- cache session labels by `(mtime, size)` (OPT2)
- `ca347c6` -- paint the TUI from a persisted snapshot, refresh in the
  background (OPT3)
- `077ab3f` -- fix: fall back to `rev-list` when the combined status call
  fails (correctness fix found while measuring OPT1)
- `0697d23` -- bound the per-worktree fan-out to `runtime.NumCPU()`
  (measured neutral, kept as a defensive bound)
- `1e2ec31` -- add a `View()` benchmark completing the time-to-first-frame
  measurement

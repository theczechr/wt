# ephemera

`wt` — worktrees that clean up after themselves.

A dashboard over every git worktree across your repos, showing which
Claude sessions and processes are running where, and bootstrapping new
worktrees with their gitignored env files and pinned submodules.

## Install

These are the steps you run yourself — nothing here is done for you
automatically.

1. Build and install the binary:

   ```bash
   cd /Users/alice/code/ephemera
   make install
   ```

   This builds `wt` and copies it to `$HOME/.local/bin/wt`. Make sure
   `~/.local/bin` is on your `PATH`.

2. Source the shell wrapper from your `~/.zshrc`. The binary can't change
   your shell's working directory on its own — see `shell/wt.zsh` for why —
   so the wrapper reads the choice `wt` leaves behind and does the `cd` for
   you. Add this line yourself:

   ```bash
   echo 'source /Users/alice/code/wt/shell/wt.zsh' >> ~/.zshrc
   exec zsh
   ```

3. Write `~/.config/wt/config.toml`. Example for a two-repo checkout:

   ```toml
   scan_roots = ["~/code"]

   [repo.api]
   env = [".env", ".env.dev", ".env.prod"]
   submodules = ["vendor/shared"]

   [repo.web]
   env = [".env", ".env.local"]
   ```

   Per-repo keys, all optional:

   | Key | Default | Purpose |
   | --- | --- | --- |
   | `env` | — | files symlinked from the primary into each new worktree |
   | `copy` | — | entries of `env` to copy instead of symlink |
   | `submodules` | — | submodules initialised at their pinned SHA |
   | `post_create` | — | shell commands run in the new worktree |
   | `ephemeral_dir` | `.worktrees` | where `wt <branch>` puts ephemeral worktrees |
   | `ephemeral_post_create` | falls back to `post_create` | lighter setup for review worktrees; set to `[]` to run nothing |
   | `default_remote` | `origin` | remote used to resolve a branch that exists only upstream |
   | `ephemeral` | `true` | set `false` to stop wt creating ephemerals for this repo |
   | `idle_days` | `3` | days an unused worktree with your own commits is kept before it counts as done (see "Cleanup"); also settable globally |

   Global keys (top level of the file):

   | Key | Default | Purpose |
   | --- | --- | --- |
   | `idle_days` | `3` | default for every repo; a `[repo.X]` value overrides it |
   | `reap_trigger_interval` | `"10m"` | minimum gap between the background sweeps that `wt` commands start (see "Triggers") |

   See
   `docs/superpowers/specs/2026-08-13-worktree-dashboard-design.md` for the
   full config reference (`copy`, `post_create`, etc).

4. (Optional) Wire up the `WorktreeCreate` hook so worktrees Claude Code
   creates get bootstrapped automatically — see below.

## Use

| Command | Effect |
| --- | --- |
| `wt` | dashboard; `⏎` cd, `r` resume a session, `/` filter, `q` quit |
| `wt status` | JSON for scripting |
| `wt new <repo> <branch>` | create a bootstrapped sibling worktree |
| `wt bootstrap <path>` | bootstrap an existing worktree (used by the hook) |
| `wt reap [path]` | remove an ephemeral worktree if it is done and passes every safety check (used by the `SessionEnd` hook when no path is given) |
| `wt reap --all [--dry-run] [--json]` | sweep every configured repo; `--dry-run` prints `reap` or `keep` plus the reason per worktree and deletes nothing |
| `wt hooks install claude [--yes]` | add the `SessionEnd` hook to `~/.claude/settings.json` after confirmation |
| `wt timer install\|uninstall\|status` | manage an hourly background sweep (launchd on macOS, systemd user timer on Linux) |
| `wt branches --unpushed` | list local branches with unpushed commits and no worktree, as `repo<TAB>branch` |
| `wt <branch>` | cd to the worktree holding that branch, creating a short-lived one if none does |
| `wt <PR>` | the same for a pull request: `wt 1234`, `wt '#1234'`, or a PR URL |
| `wt open <branch>` | the same as `wt <branch>`, for a branch whose name collides with a subcommand or is a bare number |

### Keybindings (dashboard)

| Key | Action |
| --- | --- |
| `j` / `k` (or `↓` / `↑`) | move selection |
| `⏎` | cd into the selected worktree |
| `r` | resume the Claude session running there |
| `/` | filter |
| `q` / `Ctrl-C` | quit |

There is no prune key. See "Cleanup" below for what is (and isn't) removed
automatically.

### `wt <branch>`

Paste a branch name off a GitHub PR and `wt` finds it without you knowing
which repo it lives in. It searches `refs/heads` and every remote across
*every* configured repo; a `git fetch` only happens on a miss, so the common
case (a branch you or a teammate already have refs for) costs nothing extra.
If the branch is checked out somewhere already, `wt` cds there and creates
nothing — git allows a branch in only one worktree, so this is the case you
hit most often. If it matches more than one repo, you're prompted to pick
one interactively; non-interactively (e.g. piped output) it lists the
matches and exits instead of hanging.

Otherwise `wt` creates a new worktree under the matching repo's
`ephemeral_dir` and bootstraps it exactly like `wt new`. That worktree is
**ephemeral** — see "Cleanup" below for exactly when it gets cleaned up
automatically. A repo configured with `ephemeral = false` is
still searched (an existing checkout is still useful to find), but nothing
new is created there; use `wt new <repo> <branch>` instead.

`wt open <branch>` does the same thing, and exists only so a branch
genuinely named `status`, `new`, `timer`, `1234` or another reserved word or
number is still reachable — `wt status` would otherwise be read as the JSON
subcommand, not a branch called `status`.

When wt creates a worktree for a branch that exists only on the remote, the
local branch it makes is recorded as wt-created (see "Cleanup").

## Pull requests

`wt 1234` opens pull request 1234 the way `wt <branch>` opens a branch. So
do `wt '#1234'` (quote it in zsh, where `#` starts a comment) and a full PR
URL. A bare number or `#number` is always a PR; use `wt open 1234` for a
branch that is actually named `1234`.

A bare number uses the repo containing the current directory. Outside any
configured repo it asks which repo to use, or, when there is no terminal to
ask on, exits with a hint listing the configured repos (pass the PR URL
instead). A URL picks the configured repo whose `origin` matches it. `wt` asks `gh` for the PR, so
`gh` must be installed and authenticated.

- A PR from the same repository opens its branch, exactly like `wt <branch>`.
- A PR from a fork is fetched into a local branch `pr/<N>` (its head is
  also kept at `refs/remotes/wt-pr/<N>`). It is for review; pushing back to
  the fork is out of scope.
- Re-running `wt 1234` cds to the existing worktree. If that worktree is
  clean and has no commits of yours, it first fast-forwards to the PR's
  current head. Otherwise it is left alone and `wt` tells you the PR has new
  commits.

## Wiring the `WorktreeCreate` hook

Add this to `~/.claude/settings.json` under `hooks` yourself:

```json
{
  "hooks": {
    "WorktreeCreate": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "$HOME/.local/bin/wt bootstrap \"${CLAUDE_WORKTREE_PATH:-$(cat | jq -r '.worktree_path // .path')}\""
          }
        ]
      }
    ]
  }
}
```

**The payload shape has not been verified against a real `WorktreeCreate`
firing.** Only the existence of the `WorktreeCreate` and `WorktreeRemove`
hook events (in Claude Code 2.1.229) has been confirmed — not whether the
worktree path arrives as an environment variable (`$CLAUDE_WORKTREE_PATH`) or
as JSON on stdin. The command above tries the env var first and falls back to
reading `.worktree_path` or `.path` from stdin JSON via `jq`, but this
fallback is unverified and the field names are a guess based on the
`bootstrap` subcommand's own needs, not confirmed documentation.

**Confirm this on first use**: trigger a real worktree creation, check
whether `wt bootstrap` actually ran (e.g. look for symlinked `.env*` files in
the new worktree — `ls -la <new-worktree>/.env*`), and if it didn't, run
`claude --debug` and inspect what the hook actually receives, then fix the
`command` above to match.

## Cleanup

`wt` removes exactly one kind of worktree on its own: an *ephemeral* one it
created via `wt <branch>` or `wt <PR>`. Such a worktree carries a
`.wt-ephemeral` marker file and lives inside the repo's `ephemeral_dir`
(`.worktrees/` by default). It is removed when it is **done** and safe to
remove.

### When a worktree is done

Your own commits are the ones on the worktree's branch, made since wt created
it, whose author email matches `git config user.email` (ignoring case). With
no `user.email` set, every commit counts as yours, so worktrees are kept
longer rather than removed early.

- **No commits of your own** (a review): done as soon as it is safe to remove.
- **Commits of your own**: done once it is safe to remove and either the
  branch's PR is merged or closed, or the worktree has sat unused for
  `idle_days` (default 3). "Unused" counts session activity, git index and
  `HEAD` reflog changes, and the last time `wt` opened the worktree.

For worktrees created by an older `wt`, your own commits are the ones you
authored that exist on no remote.

### What is never removed

- A worktree with uncommitted or untracked changes, including inside its
  submodules. Untracked files are checked even with
  `status.showUntrackedFiles=no`.
- A worktree whose submodules hold commits that exist on no remote. A
  submodule's commits live inside the worktree's git dir, so removal would
  delete the only copy.
- A worktree with a live Claude session, or with a process `wt` recognises
  running in it. Any running `claude` process whose working directory is in
  the worktree counts as live, whether or not its command line names a
  session (`--session-id` or `--resume`), including a pre-warmed
  `--bg-spare` process once it has been claimed.
- A worktree whose checked-out branch no longer matches the one it was
  created for.
- A worktree locked with `git worktree lock`.
- Your primary checkouts, hand-made siblings, worktrees from `wt new`, and
  any worktree whose marker is gone or unreadable.
- Anything `wt` cannot read. A git status, lock state, session list, process
  list or submodule check that fails blocks removal; it is never taken to
  mean "nothing there".

The live-process check only looks at the keywords the dashboard's process
pane uses — `deno`, `node`, `supabase`, `psql`, `docker`, `go run`, `vite`,
`next` — because resolving every process's cwd is the slow part of a refresh.
A `cargo build`, a `pytest` run, a `make`, or a plain shell is invisible to
it and will not block removal. Their output (`target/`, `.pytest_cache/`) is
usually gitignored, so it doesn't dirty the tree either, and it goes with the
worktree.

Liveness, cleanliness, unpushed submodule commits and the marker are checked
again immediately before removal, so a session opened mid-sweep, or a
worktree you reopened while a sweep was deciding about it, is kept. A worktree with
initialized submodules is removed with a single `git worktree remove
--force`, which git requires for submodules; the checks above are what make
that safe, and a lock is never overridden.

### The 15-minute grace

Sweeps — `wt reap --all`, the background sweep, the dashboard refresh and the
timer — skip any worktree created or opened less than 15 minutes ago. A
review worktree you just made has no session yet and would otherwise look
abandoned. The `SessionEnd` hook and `wt reap <path>` do not apply the grace:
they are triggered by you finishing, and run immediately.

### Branches survive

Removal is a **soft delete**: the worktree goes to the trash, so `<space>t`
can restore it. The branch is kept, with one exception: a branch that wt
itself created (`pr/<N>` for fork PRs, or a local branch made for a
remote-only `wt <branch>`) is deleted with the worktree if every commit on it
is on a remote. It can be fetched again, and restoring the worktree does so.
Anything else, including a branch you created yourself, stays.

A restored worktree loses its marker along with its directory, so it becomes
permanent and is never reaped again.

### Seeing why something is still there

```bash
wt reap --all --dry-run          # one reap/keep line with a reason per worktree
wt reap --all --dry-run --json   # the same, for scripting
```

Nothing is deleted. A dry run also lists each directory in the ephemeral dir
that has no usable marker, with the reason it is never removed. Real
decisions are appended to `~/.local/state/wt/reap.log` with their reason.

### Triggers

- `wt hooks install claude` merges a `SessionEnd` hook into
  `~/.claude/settings.json` so a worktree is collected when its session ends.
  It shows what it will add and asks first (`--yes` skips the question; the
  previous file is saved as `settings.json.wt-backup`). See "Wiring the
  `SessionEnd` hook" below.
- Any `wt` command except `reap`, `hook` (the plugin entry points), `timer`
  and `help` may start a detached background `wt reap --all`, at most once
  per `reap_trigger_interval` (default `10m`). The command you ran never
  waits for it. The dashboard does not start one; it sweeps in the background
  on each refresh instead. Two sweeps never run at once.
- `wt timer install` adds an hourly `wt reap --all`: a launchd agent on macOS,
  a systemd user timer on Linux. It is optional, but it is the only trigger
  that fires `idle_days` for a worktree nobody touches again. It runs with
  the `PATH` you had when you installed it, so it finds `gh` wherever your
  shell did; reinstall after moving `gh`.
  `wt timer status` and `wt timer uninstall` do what they say.

### Unpushed work

A branch's commits are no longer required to be pushed before its worktree
goes, since the branch stays. To find work that exists only locally:

```bash
wt branches --unpushed
```

It lists local branches with commits on no remote and no worktree.

Known gap: `git stash` writes to `refs/stash` in the shared common dir, so a
stash created inside an ephemeral worktree survives that worktree's removal.
The work is recoverable via `git stash list`, but the situation is confusing.

## Wiring the `SessionEnd` hook

`wt hooks install claude` does this for you. To do it by hand, add this to
`~/.claude/settings.json` under `hooks`:

```json
{
  "hooks": {
    "SessionEnd": [
      {
        "hooks": [
          { "type": "command", "command": "$HOME/.local/bin/wt reap", "timeout": 30 }
        ]
      }
    ]
  }
}
```

With no argument, `wt reap` reads the SessionEnd JSON from stdin and does
nothing unless the directory is an ephemeral worktree that is done and passes
every check in "Cleanup". Payload shape verified against the hook documentation on 2026-08-13:
`session_id`, `cwd`, `reason` and friends arrive as JSON on stdin.

The ending session itself is still running while its hook runs, so it does
not count as live: it is matched by the payload's `session_id`, or, when its
command line names no session, by being the nearest `claude` process among
the hook's ancestors (the one that started it). Every other session in the
worktree still blocks, including an outer `claude` further up that chain
whose own tool call started the ending one.

The hook is not guaranteed to run after a crash or `kill -9`, which is why
`wt` also sweeps in the background. Decisions are recorded in
`~/.local/state/wt/reap.log`.

## Limitations

- **zsh only.** `shell/wt.zsh` is the only shipped shell wrapper; there is no
  bash or fish equivalent yet. Porting it is mechanical — see the comment at
  the top of that file for why a wrapper is needed at all.
- **macOS and Linux, not Windows.** `wt` shells out to `git`, `gh`, `ps`, and
  `lsof`; there is no Windows process-inspection path.
- **No tmux integration.** A live session running in another terminal tab
  cannot be reattached — `r resume` can only open a new terminal at that
  path, not jump into the running one. Adopting tmux would fix this but is a
  separate decision with its own cost; see the design docs.
- **Process detection is a fixed keyword allowlist** (`deno`, `node`,
  `supabase`, `psql`, `docker`, `go run`, `vite`, `next`), chosen to keep the
  dashboard's refresh fast. A long-running process outside that list (a
  `cargo build`, a bare shell) is invisible to both the dashboard's process
  pane and the ephemeral-worktree reaper — see "Cleanup" above.
- **The `WorktreeCreate` hook payload shape is unverified**, as noted above —
  confirm it actually fires bootstrap on your setup before relying on it.
- **`git stash` survives worktree removal but is easy to lose track of**: a
  stash created inside a reaped ephemeral worktree stays in the shared
  `refs/stash`, recoverable only via `git stash list`, not through `wt`'s own
  trash/restore flow.
- **No Windows-style path handling, no multi-user/shared-machine awareness.**
  `wt` assumes a single local user working across their own repos.

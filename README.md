# wt

A dashboard over every git worktree across your repos, showing which
Claude sessions and processes are running where, and bootstrapping new
worktrees with their gitignored env files and pinned submodules.

## Install

These are the steps you run yourself — nothing here is done for you
automatically.

1. Build and install the binary:

   ```bash
   cd /Users/alice/code/wt
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
| `wt reap [path]` | remove an ephemeral worktree if it passes every safety check (used by the `SessionEnd` hook when no path is given) |
| `wt <branch>` | cd to the worktree holding that branch, creating a short-lived one if none does |
| `wt open <branch>` | the same, for a branch whose name collides with a subcommand |

### Keybindings (dashboard)

| Key | Action |
| --- | --- |
| `j` / `k` (or `↓` / `↑`) | move selection |
| `⏎` | cd into the selected worktree |
| `r` | resume the Claude session running there |
| `/` | filter |
| `q` / `Ctrl-C` | quit |

There is no prune key. See "What `wt` deletes" below for what is (and isn't)
removed automatically.

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
**ephemeral** — see "What `wt` deletes" below for exactly when it gets
cleaned up automatically. A repo configured with `ephemeral = false` is
still searched (an existing checkout is still useful to find), but nothing
new is created there; use `wt new <repo> <branch>` instead.

`wt open <branch>` does the same thing, and exists only so a branch
genuinely named `status`, `new`, or another reserved word is still
reachable — `wt status` would otherwise be read as the JSON subcommand, not
a branch called `status`.

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

## What `wt` deletes

`wt` deletes automatically exactly one thing: an *ephemeral* worktree it
created itself via `wt <branch>`. Such a worktree carries a `.wt-ephemeral`
marker file and lives inside the repo's `ephemeral_dir` (`.worktrees/` by
default). It is removed only when all of the following hold:

- the working tree is clean, and `git status` actually succeeded
- the branch has an upstream **and** no unpushed commits
- no Claude session is live in it, and none of the processes `wt` recognises
  are running there
- `git worktree remove` (never `--force`) agrees to remove it

That third point is narrower than it sounds, so read it literally. `wt` only
looks up the working directory of processes matching the same keyword list
the dashboard's process pane uses — `deno`, `node`, `supabase`, `psql`,
`docker`, `go run`, `vite`, `next` — because resolving every process's cwd is
the slow part of a refresh. A `cargo build`, a `pytest` run, a `make`, or a
plain shell sitting in an ephemeral worktree is invisible to the reap and will
not block it. That output — `target/`, `.pytest_cache/`, and similar — is
usually gitignored, so it doesn't dirty the tree either, and the reap deletes
the worktree and with it whatever build output was there; restoring from the
trash brings the worktree back and re-runs bootstrap, not those files.
Everything else that should block still does: tracked modifications,
untracked non-ignored files, unpushed commits, a branch with no upstream, and
a live Claude session.

Even then the removal is a **soft delete**: it goes to the trash, so `<space>t`
can restore it, and the branch is always left alone. A reap is unattended —
nobody is watching to catch a mistake — so it stays recoverable regardless of
`delete_mode`, which governs only the dashboard's manual, confirmed delete.

A restored worktree loses its marker along with its directory, so it becomes
permanent and is never reaped again.

Anything else — your primary checkouts, hand-made siblings, worktrees created
by `wt new`, and any worktree whose marker you deleted — is never touched
automatically. There is no general `prune` subcommand, and none should be
added.

Known gap: `git stash` writes to `refs/stash` in the shared common dir, so a
stash created inside an ephemeral worktree survives that worktree's removal.
The work is recoverable via `git stash list`, but the situation is confusing.

## Wiring the `SessionEnd` hook

Add this to `~/.claude/settings.json` under `hooks` to have ephemeral
worktrees collected when a session ends:

```json
{
  "hooks": {
    "SessionEnd": [
      {
        "hooks": [
          { "type": "command", "command": "$HOME/.local/bin/wt reap" }
        ]
      }
    ]
  }
}
```

With no argument, `wt reap` reads the SessionEnd JSON from stdin and does
nothing unless the directory is an ephemeral worktree that passes every check
above. Payload shape verified against the hook documentation on 2026-08-13:
`session_id`, `cwd`, `reason` and friends arrive as JSON on stdin.

The hook is not guaranteed to run after a crash or `kill -9`, which is why
`wt` also sweeps on launch. Refusals are recorded in
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
  pane and the ephemeral-worktree reaper — see "What `wt` deletes" above.
- **The `WorktreeCreate` hook payload shape is unverified**, as noted above —
  confirm it actually fires bootstrap on your setup before relying on it.
- **`git stash` survives worktree removal but is easy to lose track of**: a
  stash created inside a reaped ephemeral worktree stays in the shared
  `refs/stash`, recoverable only via `git stash list`, not through `wt`'s own
  trash/restore flow.
- **No Windows-style path handling, no multi-user/shared-machine awareness.**
  `wt` assumes a single local user working across their own repos.

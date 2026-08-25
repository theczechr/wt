// Command wt lists git worktrees with their Claude sessions and processes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theczechr/wt/internal/aggregate"
	"github.com/theczechr/wt/internal/bootstrap"
	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/ephemeral"
	"github.com/theczechr/wt/internal/herdr"
	"github.com/theczechr/wt/internal/model"
	"github.com/theczechr/wt/internal/resolve"
	"github.com/theczechr/wt/internal/trash"
	"github.com/theczechr/wt/internal/ui"
)

// collectTimeout bounds the read-only paths: `status` and the interactive
// TUI. Both only ever call aggregate.Collect (worktree discovery, git
// status, PR lookups, the process/session snapshot), which takes about 2s in
// practice; 30s is ample headroom while still guaranteeing the process exits
// instead of hanging forever on a stale mount or an unresponsive external
// command (lsof, git, ...).
const collectTimeout = 30 * time.Second

// mutateTimeout bounds `bootstrap` and `new`, which do real work beyond a
// Collect: `git submodule update --init --recursive` (a cold submodule
// fetch), `git submodule status`, every post_create hook (arbitrary shell,
// commonly a package-manager install), and `git worktree add`. These
// routinely run past collectTimeout's 30s budget -- on this repo's `corelib`
// submodule plus a post_create install step, comfortably so -- and
// exec.CommandContext would otherwise kill the command mid-run, leaving a
// worktree that already exists on disk with a half-initialised submodule
// inside it. 15 minutes is a generous budget for a cold clone plus an
// install while still guaranteeing the process eventually exits.
const mutateTimeout = 15 * time.Minute

func main() {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt: config:", err)
		os.Exit(1)
	}

	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "status":
		// defer would run at the end of main, not the end of this case --
		// harmless here since nothing follows the switch, but cancel()
		// called right after ctx's last use is correct regardless of that.
		ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
		collected, _ := aggregate.Collect(ctx, cfg)
		ws := nonNilWorkspaces(collected)
		cancel()
		sort.Slice(ws, func(i, j int) bool {
			if ws[i].Repo != ws[j].Repo {
				return ws[i].Repo < ws[j].Repo
			}
			return ws[i].Path < ws[j].Path
		})
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(ws); err != nil {
			fmt.Fprintln(os.Stderr, "wt:", err)
			os.Exit(1)
		}
	case "bootstrap":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: wt bootstrap <worktree-path>")
			os.Exit(2)
		}
		target := os.Args[2]
		repoName, primary, err := resolveRepo(cfg, target)
		if err != nil {
			// A worktree outside any configured repo is not an error; the
			// hook fires for every worktree Claude creates anywhere.
			fmt.Fprintln(os.Stderr, "wt: skipping,", err)
			return
		}
		// Bootstrapping the primary checkout is never legitimate: it is
		// where the source env files live, so src and dst would be
		// identical for every entry. Refuse outright rather than rely
		// solely on LinkEnv's own per-entry guard, since this is also the
		// path a misconfigured WorktreeCreate hook payload (README: the
		// real shape is unverified) would take if it ever carried the
		// project root instead of the new worktree's path.
		bootstrapPath(cfg, repoName, primary, target)
	case "hook":
		// Plugin/hook entrypoints, dispatched by name so one reserved word
		// covers every integration rather than one per host tool.
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: wt hook herdr-worktree-created|herdr-startup|worktree-removed")
			os.Exit(2)
		}
		switch os.Args[2] {
		case "herdr-worktree-created":
			herdrWorktreeCreated(cfg)
		case "herdr-startup":
			herdrStartup(cfg)
		case "worktree-removed":
			worktreeRemoved()
		default:
			fmt.Fprintf(os.Stderr, "wt: hook: unknown hook %q\n", os.Args[2])
			os.Exit(2)
		}
	case "new":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: wt new <repo> <branch> [name]")
			os.Exit(2)
		}
		repoName, branch := os.Args[2], os.Args[3]
		name := ""
		if len(os.Args) > 4 {
			name = os.Args[4]
		}
		r, ok := cfg.Repos[repoName]
		if !ok {
			fmt.Fprintf(os.Stderr, "wt: repo %q is not configured\n", repoName)
			os.Exit(1)
		}
		primary := primaryPath(cfg, repoName)
		if primary == "" {
			fmt.Fprintf(os.Stderr, "wt: could not find repo %q on disk under any scan root %v\n", repoName, cfg.ScanRoots)
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
		target, err := bootstrap.Create(ctx, primary, repoName, branch, name, r)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "wt:", err)
			os.Exit(1)
		}
		fmt.Println(target)
	case "reap":
		// reap does mutate -- Reap can reach `git worktree remove` and a
		// trash-manifest write -- but collectTimeout is still the right
		// budget, not mutateTimeout: mutateTimeout's 15 minutes is sized for
		// a cold submodule clone plus post_create installs, which is absurd
		// here. A reap is one `git worktree remove` and a small manifest
		// write, both fast, and it runs from a SessionEnd hook, so it must
		// not hang a session's exit.
		ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
		defer cancel()
		target := ""
		// endingSession is the one session Reap must not count as live. A
		// SessionEnd hook runs while its own session is still alive -- Claude
		// forks this process and waits for it -- so `ps` reports that session
		// and, without naming it here, every hook-driven reap would refuse
		// itself. Empty for `wt reap <path>`, where no session is ending.
		endingSession := ""
		if len(os.Args) > 2 {
			target = os.Args[2]
		} else {
			// No argument means hook mode: the SessionEnd payload arrives on
			// stdin.
			ev, err := ephemeral.ParseSessionEnd(os.Stdin)
			if err != nil {
				fmt.Fprintln(os.Stderr, "wt: reap:", err)
				os.Exit(1)
			}
			if !ephemeral.ShouldReap(ev.Reason) {
				return
			}
			target, endingSession = ev.Cwd, ev.SessionID
		}
		root, err := ephemeral.WorktreeRoot(ctx, target)
		if err != nil {
			// Not an error worth an exit code: the hook fires for every
			// session, most of which are nowhere near an ephemeral worktree.
			return
		}
		if _, err := ephemeral.ReadMarker(root); err != nil {
			return // not one of ours; say nothing
		}
		err = ephemeral.Reap(ctx, cfg, root, endingSession)
		ephemeral.LogReap(root, err)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wt: reap:", err)
			os.Exit(1)
		}
		fmt.Println("reaped", root)
	case "":
		// Trash expiry runs before the TUI paints anything: it only ever
		// drops manifest records for worktrees `git worktree remove`
		// already deleted at soft-delete time (see trash.PurgeExpired), so
		// there is nothing to confirm -- but the user still gets a plain
		// report of exactly what was forgotten.
		purged := trash.PurgeExpired(cfg.EffectiveTrashRetention(), time.Now())
		reportPurgedTrash(purged)

		// Paint immediately from whatever was persisted by the last
		// Collect (either subcommand writes one), rather than blocking on
		// a fresh one -- that block is the ~1-2s the user actually feels
		// on every launch. A snapshot always means Run needs to kick a
		// background refresh right away; no snapshot (first run, or the
		// cache dir was cleared) falls back to today's behaviour, collect
		// then paint, so an empty dashboard is never shown.
		snapshotLoadStart := time.Now()
		ws, needsRefresh := aggregate.LoadSnapshot()
		// unresolvedProcs has no snapshot-backed value (see Collect's own
		// comment on why it's excluded from WriteSnapshot): a snapshot
		// paint starts at 0 -- no warning -- until the background refresh
		// below lands with the real count.
		var unresolvedProcs int
		if !needsRefresh {
			ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
			ws, unresolvedProcs = aggregate.Collect(ctx, cfg)
			cancel()
		}
		if os.Getenv("WT_DEBUG_TIMING") != "" {
			fmt.Fprintf(os.Stderr, "wt: time to paint-ready data: %s (from snapshot: %v)\n",
				time.Since(snapshotLoadStart), needsRefresh)
		}
		refresh := func() ([]model.Workspace, int) {
			ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
			defer cancel()
			// Deliberately inside refresh, not on the launch path: PERF.md's
			// whole point is that the first frame paints from a snapshot
			// without waiting on git. Sweeping here costs the user nothing
			// they can see.
			ephemeral.Sweep(ctx, cfg, resolve.Primaries(cfg))
			return aggregate.Collect(ctx, cfg)
		}
		ops := ui.TrashOps{
			Entries: trash.Load(),
			Mode:    cfg.EffectiveDeleteMode(),
			Delete:  makeDeleteFunc(cfg),
			Restore: makeRestoreFunc(cfg),
			Purge:   func(e trash.Entry) error { return trash.Remove(e) },
		}
		path, action, session, err := ui.Run(ws, needsRefresh, unresolvedProcs, refresh, ops)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wt:", err)
			os.Exit(1)
		}
		switch action {
		case ui.ActionCd:
			handOff(path, "")
		case ui.ActionResume:
			handOff(path, session)
		case ui.ActionNew:
			// path is a BRANCH NAME here, not a directory: the worktree does
			// not exist yet. Created on the same code path `wt <branch>`
			// uses, so there is one implementation of create -- and outside
			// the TUI, which has already exited, because a submodule clone
			// plus post_create can run for minutes and its output is what
			// the user wants to watch.
			created, err := openBranch(cfg, path)
			if err != nil {
				fmt.Fprintln(os.Stderr, "wt:", err)
				os.Exit(1)
			}
			handOff(created, "")
		}
	case "open":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: wt open <branch>")
			os.Exit(2)
		}
		openAndCd(cfg, os.Args[2])
	case "help", "-h", "--help":
		// Asking for help is not a usage error: it goes to stdout so it can be
		// piped or paged, and exits 0 so `wt --help && ...` works. The default
		// arm below is the failure path and keeps stderr/2.
		fmt.Println(usage)
	default:
		// Unreachable while every word in reservedCommands has its own case
		// above, and kept for the moment one does not: a reserved word with no
		// case would otherwise fall through to openAndCd and be looked up as a
		// branch name, which is precisely what reserving it was meant to stop.
		if isReservedCommand(cmd) {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		openAndCd(cfg, cmd)
	}
}

// usage is the one-line summary printed by `wt help` and by the unknown
// -reserved-word failure path.
const usage = "usage: wt [status|new|bootstrap|reap|hook <name>|open <branch>|<branch>]"

// nonNilWorkspaces normalises a nil slice to an empty one. wt status is a
// scripting interface, and encoding/json marshals a nil slice as `null`,
// which breaks a naive `jq '.[]'`; an empty result must still encode as `[]`.
func nonNilWorkspaces(ws []model.Workspace) []model.Workspace {
	if ws == nil {
		return []model.Workspace{}
	}
	return ws
}

// writeChoice hands the selection to the zsh wrapper, which performs the
// cd. No process can change its parent shell's working directory, so the
// TUI writes the chosen path to $TMPDIR/wt-cd and exits; the wrapper reads
// it after wt returns. For a resume, the session id is appended so the
// wrapper knows which Claude session to resume.
func writeChoice(path, sessionID string) {
	if path == "" {
		return
	}
	body := path
	if sessionID != "" {
		body += "\t" + sessionID
	}
	target := filepath.Join(os.TempDir(), "wt-cd")
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
	}
}

// handOff acts on the worktree the user chose, by whichever route can
// actually reach them.
//
// From a shell -- inside a herdr pane or outside one -- that is the choice
// file the zsh wrapper reads, because no process can change its parent
// shell's directory. Opened as a herdr plugin pane there is no parent shell:
// herdr runs the command directly and closes the overlay when it exits, so
// the file would be written and never read, and pressing enter would appear
// to do nothing. There the choice goes back to herdr, which can open and
// focus the worktree itself.
//
// A resume is deliberately not symmetrical between the two routes. The
// wrapper always runs `claude --resume`, since a shell can only ever be in
// one place. Under herdr, a worktree that was ALREADY open is only focused:
// whatever is running in that workspace is far more likely to be the session
// the user meant than a second copy started underneath it. That is also the
// case the original design could not handle at all -- without something
// owning the pty there was no way to reach a session running in another
// terminal, and focusing herdr's workspace is exactly that.
func handOff(path, sessionID string) {
	if !herdr.InPluginPane() {
		writeChoice(path, sessionID)
		return
	}
	if path == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
	defer cancel()

	opened, err := herdr.OpenWorktree(ctx, path)
	if err != nil {
		// The overlay is closing as this process exits, so this line is for
		// `herdr plugin log` rather than for the screen.
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
	if sessionID == "" || opened.AlreadyOpen {
		return
	}
	if err := herdr.ResumeClaude(ctx, opened.PaneID, sessionID, filepath.Base(path)); err != nil {
		fmt.Fprintln(os.Stderr, "wt: opened", path, "but could not resume:", err)
		os.Exit(1)
	}
}

// reservedCommands are the words that name a subcommand rather than a
// branch. `wt open <branch>` exists so a branch whose name collides with one
// of these is still reachable.
var reservedCommands = map[string]bool{
	"status": true, "new": true, "bootstrap": true, "reap": true,
	"open": true, "hook": true, "help": true, "-h": true, "--help": true,
}

func isReservedCommand(arg string) bool { return reservedCommands[arg] }

// openAndCd resolves a branch and hands the resulting path to the shell
// wrapper, which performs the cd.
func openAndCd(cfg config.Config, branch string) {
	path, err := openBranch(cfg, branch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
	handOff(path, "")
}

// openBranch resolves branch to a worktree, creating an ephemeral one when
// nothing has it checked out, and returns the path to cd into.
func openBranch(cfg config.Config, branch string) (string, error) {
	if err := resolve.ValidBranchName(branch); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
	defer cancel()

	primaries := resolve.Primaries(cfg)
	if len(primaries) == 0 {
		return "", fmt.Errorf("no configured repo found under any scan root %v", cfg.ScanRoots)
	}

	matches := resolve.Find(ctx, cfg, primaries, branch)
	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "wt: %s not found locally, fetching...\n", branch)
		resolve.Fetch(ctx, primaries)
		matches = resolve.Find(ctx, cfg, primaries, branch)
	}
	switch {
	case len(matches) == 0:
		return "", fmt.Errorf("branch %q not found in any of %v (after fetching).\n"+
			"If you meant a subcommand, the reserved words are: status, new, bootstrap, reap, open",
			branch, repoNames(primaries))
	case len(matches) > 1:
		var err error
		matches, err = pickOne(matches, branch)
		if err != nil {
			return "", err
		}
	}
	m := matches[0]

	// Git allows a branch in only one worktree, so an existing checkout is
	// not merely a shortcut -- creating another would fail. It is also the
	// case the user hits most: the branch is already open somewhere.
	if m.ExistingWorktree != "" {
		return m.ExistingWorktree, nil
	}

	r := cfg.Repos[m.Repo]
	if !r.EphemeralEnabled() {
		return "", fmt.Errorf("repo %q has ephemeral = false; use `wt new %s %s` to create a permanent worktree",
			m.Repo, m.Repo, branch)
	}

	target := filepath.Join(m.Primary, r.EphemeralDirOrDefault(), bootstrap.Slug(branch))
	startPoint := ""
	if !m.Local {
		// Invariant, not a reachable branch today: resolve.Find only ever
		// sets `found` alongside either Local=true or a non-empty Remote, so
		// !m.Local && m.Remote == "" cannot occur. Asserted here anyway
		// rather than left to fall through to CreateAt's own default (branch
		// from the primary's HEAD), because that default is silently wrong
		// for this call site -- see CreateAt's doc comment -- and a future
		// change to resolve.Find's invariant must not resurrect that bug
		// quietly.
		if m.Remote == "" {
			return "", fmt.Errorf("branch %q in %s has neither a local nor a remote ref", branch, m.Repo)
		}
		startPoint = m.Remote + "/" + branch
	}

	// Written before the worktree exists so the primary's status is never
	// briefly polluted, and so a later failure still leaves the repo tidy.
	if err := ephemeral.EnsureExcluded(ctx, m.Primary,
		[]string{r.EphemeralDirOrDefault() + "/", ephemeral.MarkerName}); err != nil {
		return "", err
	}
	if err := bootstrap.CreateAt(ctx, m.Primary, target, branch, startPoint, r.ForEphemeral()); err != nil {
		// The worktree this left on disk is deliberately unmarked (no
		// .wt-ephemeral) so the reaper never touches it, but that also means
		// nothing else will ever clean it up -- tell the user where it is.
		return "", fmt.Errorf("bootstrapping %s: %w (left in place for you to inspect or remove)", target, err)
	}
	// Only after a successful bootstrap: a half-set-up worktree is left for
	// the user to inspect rather than being silently collected later.
	if err := ephemeral.WriteMarker(target, ephemeral.Marker{
		Repo:      m.Repo,
		Branch:    branch,
		Primary:   m.Primary,
		CreatedAt: time.Now(),
	}); err != nil {
		return "", fmt.Errorf("created %s but could not mark it ephemeral: %w", target, err)
	}
	return target, nil
}

func pickOne(matches []resolve.Match, branch string) ([]resolve.Match, error) {
	if !ui.Interactive() {
		var names []string
		for _, m := range matches {
			names = append(names, m.Repo)
		}
		return nil, fmt.Errorf("branch %q exists in %s; run `wt new <repo> %s` or rerun in a terminal",
			branch, strings.Join(names, ", "), branch)
	}
	labels := make([]string, len(matches))
	for i, m := range matches {
		where := "local"
		if !m.Local {
			where = m.Remote
		}
		labels[i] = fmt.Sprintf("%-14s %s", m.Repo, where)
	}
	idx, err := ui.Choose("branch "+branch+" is in more than one repo", labels)
	if err != nil {
		return nil, err
	}
	if idx < 0 {
		return nil, fmt.Errorf("cancelled")
	}
	return matches[idx : idx+1], nil
}

func repoNames(primaries map[string]string) []string {
	names := make([]string, 0, len(primaries))
	for n := range primaries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// pluginID and dashboardEntrypoint mirror herdr-plugin.toml. They are
// constants rather than config because they name entries in a manifest that
// ships in this repo: if either drifts, the manifest is wrong, not the
// user's setup.
const (
	pluginID            = "theczechr.wt"
	dashboardEntrypoint = "dashboard"
)

// herdrStartup is herdr's [[startup]] hook: it decides whether to open the
// dashboard by itself when herdr starts.
//
// The "auto" default is the interesting part. LazyVim shows its dashboard
// whenever nvim opens without a file, which is almost always, because an
// editor starts empty. herdr does not: startup hooks run AFTER it restores
// the previous session, so it usually comes up with agents already working.
// Opening an overlay across that on every start -- and every
// `herdr update --handoff`, which re-runs startup hooks -- would be noise.
// So auto opens the dashboard only when no agent is running, which is when
// a launcher is what the user actually wanted.
//
// Failures are reported and swallowed. herdr documents that a startup
// failure does not stop the server, and a dashboard that did not appear must
// never be the reason a session fails to come up.
func herdrStartup(cfg config.Config) {
	mode := cfg.EffectiveHerdrStartupDashboard()
	if mode == config.StartupNever {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()

	if mode == config.StartupAuto {
		session, err := herdr.Snapshot(ctx)
		if err != nil {
			// Unknown state: do nothing. An unwanted overlay over live work
			// is worse than a missing convenience, so the uncertain case
			// declines to act rather than guessing.
			fmt.Fprintln(os.Stderr, "wt: startup: skipping,", err)
			return
		}
		if session.Busy() {
			return
		}
	}
	if err := herdr.OpenDashboard(ctx, pluginID, dashboardEntrypoint); err != nil {
		fmt.Fprintln(os.Stderr, "wt: startup:", err)
	}
}

// worktreeRemoved drops a removed worktree from the paint snapshot, so the
// next launch does not show a row for a directory that is gone.
//
// It accepts the path from whichever source fired it, because two different
// systems announce the same event and only one of them is verified:
// herdr's worktree.removed carries a typed, schema-checked payload in
// HERDR_PLUGIN_EVENT_JSON, while Claude Code's WorktreeRemove hook has a
// payload shape this project has never confirmed (see README). An explicit
// argv path is the third and simplest form, and the one a script should use.
//
// Doing nothing is the correct outcome for an unrecognised payload: this
// only ever prunes a UI cache that the next refresh rebuilds anyway, so
// there is no failure here worth an exit code.
func worktreeRemoved() {
	path := ""
	if len(os.Args) > 3 {
		path = os.Args[3]
	}
	if path == "" {
		path = os.Getenv("CLAUDE_WORKTREE_PATH")
	}
	if path == "" {
		if wt, err := herdr.ParseWorktreeRemoved(os.Getenv("HERDR_PLUGIN_EVENT_JSON")); err == nil {
			path = wt.Path
		}
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "wt: hook: no worktree path in argv, CLAUDE_WORKTREE_PATH, or HERDR_PLUGIN_EVENT_JSON")
		return
	}
	dropped, err := aggregate.DropFromSnapshot(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt: hook:", err)
		return
	}
	if dropped {
		fmt.Println("dropped", path, "from the snapshot")
	}
}

// bootstrapPath runs a bootstrap and exits non-zero on failure. Shared by
// `wt bootstrap <path>` and every hook entrypoint, so a worktree created by
// herdr is prepared by exactly the same code -- and refused by exactly the
// same guards -- as one created by hand.
func bootstrapPath(cfg config.Config, repoName, primary, target string) {
	// Bootstrapping the primary checkout is never legitimate: it is where
	// the source env files live, so src and dst would be identical for every
	// entry. Refuse outright rather than rely solely on LinkEnv's own
	// per-entry guard, since a hook payload naming the wrong path is exactly
	// how this call site would be reached.
	if bootstrap.SamePath(primary, target) {
		fmt.Fprintf(os.Stderr, "wt: bootstrap: refusing to bootstrap %s -- it is the primary checkout, not a worktree\n", target)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
	err := bootstrap.Run(ctx, primary, target, cfg.Repos[repoName])
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt: bootstrap:", err)
		os.Exit(1)
	}
	fmt.Printf("bootstrapped %s from %s\n", target, primary)
}

// herdrWorktreeCreated is the `worktree.created` plugin event hook. Herdr
// creates the worktree; wt supplies the bootstrap herdr has no concept of.
//
// Exit codes carry meaning here, because herdr surfaces plugin command
// failures in its logs and a hook that cries wolf gets ignored:
//
//   - a payload for some other event, or a worktree in a repo wt does not
//     manage, exits 0 and says why on stderr. Neither is a fault; the hook
//     fires for every worktree herdr creates, most of which are not ours.
//   - a malformed or absent payload exits non-zero. That means the wiring is
//     broken -- wrong env var, a herdr protocol change -- and silence would
//     leave worktrees quietly unbootstrapped, which is the failure this hook
//     exists to prevent.
func herdrWorktreeCreated(cfg config.Config) {
	wt, err := herdr.ParseWorktreeCreated(os.Getenv("HERDR_PLUGIN_EVENT_JSON"))
	if err != nil {
		if errors.Is(err, herdr.ErrNotWorktreeCreated) {
			fmt.Fprintln(os.Stderr, "wt: hook: ignoring,", err)
			return
		}
		fmt.Fprintln(os.Stderr, "wt: hook:", err)
		os.Exit(1)
	}
	repoName, primary, err := resolveRepo(cfg, wt.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt: hook: skipping,", err)
		return
	}
	bootstrapPath(cfg, repoName, primary, wt.Path)
}

// resolveRepo finds which configured repo a worktree belongs to by asking git
// for its common directory, which points into the primary checkout.
func resolveRepo(cfg config.Config, target string) (repoName, primary string, err error) {
	out, err := exec.Command("git", "--no-optional-locks", "-C", target, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", "", fmt.Errorf("%s is not a git worktree", target)
	}
	primary = filepath.Dir(strings.TrimSpace(string(out))) // strip trailing /.git
	name := filepath.Base(primary)
	if _, ok := cfg.Repos[name]; !ok {
		return "", "", fmt.Errorf("repo %q is not configured", name)
	}
	return name, primary, nil
}

// primaryPath returns the primary checkout for a configured repo.
func primaryPath(cfg config.Config, repoName string) string {
	for _, root := range cfg.ScanRoots {
		p := filepath.Join(root, repoName)
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p
		}
	}
	return ""
}

// reportPurgedTrash prints a plain-text summary of what trash.PurgeExpired
// dropped, to stderr, before the TUI starts. Silent when nothing was
// purged.
func reportPurgedTrash(purged []trash.Entry) {
	if len(purged) == 0 {
		return
	}
	plural := ""
	if len(purged) != 1 {
		plural = "s"
	}
	fmt.Fprintf(os.Stderr, "purged %d expired worktree%s from trash:\n", len(purged), plural)
	now := time.Now()
	for _, e := range purged {
		fmt.Fprintf(os.Stderr, "  %-18s %-18s %s\n", filepath.Base(e.Path), e.Branch, e.Age(now))
	}
	fmt.Fprintln(os.Stderr, "branches kept; nothing else removed")
}

// makeDeleteFunc wires the UI's "d d" flow to trash.SoftDelete or
// trash.HardDelete, chosen once from cfg.EffectiveDeleteMode() -- the UI
// itself never decides the mode, only displays it.
func makeDeleteFunc(cfg config.Config) ui.DeleteFunc {
	mode := cfg.EffectiveDeleteMode()
	return func(ws model.Workspace) (*trash.Entry, string, error) {
		primary := primaryPath(cfg, ws.Repo)
		if primary == "" {
			return nil, "", fmt.Errorf("could not resolve primary checkout for repo %q", ws.Repo)
		}
		ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
		defer cancel()

		if mode == "hard" {
			res, err := trash.HardDelete(ctx, ws, primary)
			if err != nil {
				return nil, "", err
			}
			if !res.BranchDeleted {
				return nil, fmt.Sprintf("worktree removed; branch %q kept (unmerged): %s", ws.Branch, res.BranchKeptReason), nil
			}
			return nil, "", nil
		}

		e, err := trash.SoftDelete(ctx, ws, primary)
		if err != nil {
			return nil, "", err
		}
		return &e, "", nil
	}
}

// makeRestoreFunc wires the trash view's "u" key to trash.Restore, using
// whichever config.Repo the entry's own repo name resolves to today (an
// empty Repo{} when the repo is no longer configured, matching wt new's own
// tolerance for a repo without special bootstrap needs).
func makeRestoreFunc(cfg config.Config) ui.RestoreFunc {
	return func(e trash.Entry) error {
		r := cfg.Repos[e.Repo]
		ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
		defer cancel()
		return trash.Restore(ctx, e, r)
	}
}

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

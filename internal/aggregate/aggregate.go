// Package aggregate merges the collectors into Workspace values.
package aggregate

import (
	"context"
	"errors"
	"fmt"
	"github.com/theczechr/wt/internal/herdr"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/discover"
	"github.com/theczechr/wt/internal/model"
)

const prTTL = 10 * time.Minute

// Attribute assigns each process to the deepest worktree containing its cwd,
// and marks sessions live from the id->pid map.
func Attribute(ws []model.Workspace, procs []model.Proc, live map[string]int) []model.Workspace {
	for _, p := range procs {
		best := -1
		for i := range ws {
			if p.Cwd == ws[i].Path || strings.HasPrefix(p.Cwd, ws[i].Path+"/") {
				if best == -1 || len(ws[i].Path) > len(ws[best].Path) {
					best = i
				}
			}
		}
		if best >= 0 {
			ws[best].Procs = append(ws[best].Procs, p)
		}
	}
	for i := range ws {
		for j := range ws[i].Sessions {
			if pid, ok := live[ws[i].Sessions[j].ID]; ok {
				ws[i].Sessions[j].Live = true
				ws[i].Sessions[j].PID = pid
			}
		}
	}
	return ws
}

// discoverRepos finds git repositories directly under the scan roots.
func discoverRepos(cfg config.Config) map[string]string {
	repos := map[string]string{}
	for _, root := range cfg.ScanRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
				continue
			}
			// Only the repo whose name matches config is treated as primary;
			// its siblings are found via git worktree list, not by scanning.
			if _, ok := cfg.Repos[e.Name()]; ok {
				repos[e.Name()] = p
			}
		}
	}
	return repos
}

// agentProbe pairs herdr's agent index with how the lookup went, so the
// absent/unreadable distinction survives into every workspace.
type agentProbe struct {
	idx   herdr.Index
	state model.AgentProbe
}

// applyAgents joins herdr's agents onto the collected worktrees.
//
// The probe state is stamped on every workspace even when it is Absent or
// Unreadable, because those are facts about the lookup rather than about any
// one worktree -- and PruneBlockers needs Unreadable to reach it, or a
// daemon that stopped answering would silently read as "nothing running".
func applyAgents(ws []model.Workspace, probe agentProbe) {
	for i := range ws {
		ws[i].AgentProbe = probe.state
	}
	if probe.state != model.AgentProbeOK {
		return
	}
	// Attributed against the whole set at once, not per workspace: nested
	// worktrees live inside the primary checkout, so an agent must be
	// assigned to the most specific worktree containing it rather than to
	// every worktree that happens to contain it.
	paths := make([]string, len(ws))
	for i := range ws {
		paths[i] = ws[i].Path
	}
	byPath := probe.idx.Attribute(paths)
	for i := range ws {
		agents := byPath[ws[i].Path]
		ws[i].AgentCount = len(agents)
		ws[i].AgentStatus = herdr.Worst(agents)
	}
}

// Collect runs every collector concurrently and returns merged workspaces,
// plus a count of processes whose cwd could not be resolved during this
// run's process snapshot (see discover.Processes.UnresolvedCwds) -- callers
// that surface deletion decisions to a human (the dashboard's "d d" flow)
// use this to warn rather than silently under-report what's running.
func Collect(ctx context.Context, cfg config.Config) ([]model.Workspace, int) {
	repos := discoverRepos(cfg)

	// Loaded once per Collect and shared read-mostly across every
	// per-worktree goroutine below; Flush writes it back once, after all of
	// them finish, instead of once per worktree.
	sessionCache := discover.LoadSessionCache()

	// Bounds how many per-worktree collectors (each forking a git process)
	// run at once, across ALL repos combined -- one shared semaphore, not
	// one per repo, so N repos collecting concurrently can't each spin up
	// their own NumCPU()-sized pool and stack up to N*NumCPU() processes.
	// Unbounded, this fans out one goroutine per worktree (71 on this
	// machine) all at once; see PERF.md for the measurement that decided
	// this was worth keeping.
	sem := make(chan struct{}, runtime.NumCPU())

	var (
		mu  sync.Mutex
		all []model.Workspace
		wg  sync.WaitGroup
	)
	for name, path := range repos {
		wg.Add(1)
		go func(name, path string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "wt: recovered panic collecting repo %q (%s): %v\n", name, path, r)
				}
			}()
			ws, err := discover.Worktrees(ctx, path, name)
			if err != nil {
				return
			}
			prs, ok := discover.CachedPRs(path, prTTL)
			if !ok {
				if fresh, err := discover.PRsForRepo(ctx, path); err == nil {
					prs = fresh
					_ = discover.WritePRCache(path, fresh)
				}
			}
			var inner sync.WaitGroup
			for i := range ws {
				inner.Add(1)
				sem <- struct{}{}
				go func(w *model.Workspace) {
					defer inner.Done()
					defer func() { <-sem }()
					defer func() {
						if r := recover(); r != nil {
							fmt.Fprintf(os.Stderr, "wt: recovered panic collecting worktree %s: %v\n", w.Path, r)
						}
					}()
					discover.FillStatus(ctx, w)
					w.Sessions = discover.SessionsFor(w.Path, sessionCache)
					if pr, ok := prs[w.Branch]; ok {
						w.PR = pr
					} else {
						for _, s := range w.Sessions {
							if s.PRNumber != 0 {
								w.PR = model.PR{Number: s.PRNumber, State: "?"}
								break
							}
						}
					}
					for _, s := range w.Sessions {
						if s.Mtime.After(w.LastUsed) {
							w.LastUsed = s.Mtime
						}
					}
				}(&ws[i])
			}
			inner.Wait()
			mu.Lock()
			all = append(all, ws...)
			mu.Unlock()
		}(name, path)
	}

	// Asked once per Collect, concurrently with the process snapshot, and
	// joined onto every worktree below. herdr answers over a local socket,
	// so this costs far less than the git fan-out it runs beside.
	agentsCh := make(chan agentProbe, 1)
	go func() {
		var res agentProbe
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "wt: recovered panic reading herdr agents: %v\n", r)
				// A panic is not evidence herdr is absent. Report unknown.
				res = agentProbe{state: model.AgentProbeUnreadable}
			}
			agentsCh <- res
		}()
		idx, err := herdr.Agents(ctx)
		switch {
		case err == nil:
			res = agentProbe{idx: idx, state: model.AgentProbeOK}
		case errors.Is(err, herdr.ErrNotRunning):
			// Absent, not unknown: no daemon means genuinely no agents.
			res = agentProbe{state: model.AgentProbeAbsent}
		default:
			// herdr is up but would not answer. Unknown, and the delete gate
			// must treat it as such rather than as "nothing running here".
			fmt.Fprintln(os.Stderr, "wt: herdr:", err)
			res = agentProbe{state: model.AgentProbeUnreadable}
		}
	}()

	procsCh := make(chan []model.Proc, 1)
	liveCh := make(chan map[string]int, 1)
	unresolvedCh := make(chan int, 1)
	go func() {
		// Collect does an unconditional <-procsCh / <-liveCh / <-unresolvedCh
		// below, so this goroutine holds the only sends on all three
		// channels. Unlike the per-repo/per-worktree goroutines above, a
		// naive recover() here would still deadlock Collect forever, since a
		// panic mid-Snapshot would skip the sends entirely. All three
		// channels are buffered and guaranteed a value no matter what.
		//
		// SnapshotErr, not Snapshot, so this same one call can also report
		// how many processes' cwds could not be resolved -- see
		// discover.Processes.UnresolvedCwds. The ps-level error SnapshotErr
		// can also return is deliberately still swallowed here, exactly as
		// Snapshot did: this is the dashboard's fast paint path, and a
		// caller that must treat "ps failed" as unsafe-to-delete
		// (ephemeral.Reap) already uses SnapshotErr directly for that.
		var p []model.Proc
		var l map[string]int
		var unresolved int
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "wt: recovered panic in Snapshot: %v\n", r)
			}
			procsCh <- p
			liveCh <- l
			unresolvedCh <- unresolved
		}()
		procs, _ := discover.SnapshotErr(ctx)
		p, l, unresolved = procs.Procs, procs.Live, procs.UnresolvedCwds
	}()

	wg.Wait()
	// Best-effort: a failed write here (permissions, disk full, ...) must
	// not fail Collect. It just means the next run pays the live-read cost
	// again -- caching is a pure optimisation, never a correctness
	// dependency.
	_ = sessionCache.Flush()

	result := Attribute(all, <-procsCh, <-liveCh)
	unresolvedProcs := <-unresolvedCh
	applyAgents(result, <-agentsCh)
	// Persist for the next TUI launch to paint from immediately. Same
	// best-effort contract as the session cache above. The unresolved-cwd
	// count is deliberately NOT part of the snapshot: it is a fact about
	// this run's process probe, not about any workspace, and would be stale
	// the moment it was read back on the next launch.
	_ = WriteSnapshot(result)
	return result, unresolvedProcs
}

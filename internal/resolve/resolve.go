// Package resolve finds which configured repository a branch name belongs to.
package resolve

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/theczechr/wt/internal/config"
	"github.com/theczechr/wt/internal/discover"
)

// Match is one repository that has the branch.
type Match struct {
	Repo    string
	Primary string
	// Local is true when refs/heads/<branch> exists in this repo.
	Local bool
	// Remote names the remote holding the branch, empty when only a local
	// ref exists.
	Remote string
	// ExistingWorktree is the path of a worktree that already has the branch
	// checked out. Git allows only one, so when this is set wt must use it
	// rather than create anything.
	ExistingWorktree string
}

// ValidBranchName rejects names git would refuse or that would be unsafe to
// pass to a command line. The name arrives by paste from a browser, so this
// is ordinary input validation, not paranoia: a leading dash would be read as
// a flag, and the rest mirror git's own check-ref-format rules.
func ValidBranchName(branch string) error {
	switch {
	case branch == "":
		return fmt.Errorf("empty branch name")
	case strings.HasPrefix(branch, "-"):
		return fmt.Errorf("branch %q may not start with '-'", branch)
	case strings.HasSuffix(branch, ".lock"), strings.HasSuffix(branch, "/"),
		strings.HasSuffix(branch, "."):
		return fmt.Errorf("branch %q has an invalid suffix", branch)
	case strings.Contains(branch, ".."), strings.Contains(branch, "//"):
		return fmt.Errorf("branch %q contains an invalid sequence", branch)
	}
	if strings.ContainsAny(branch, " \t\n~^:?*[]\\") {
		return fmt.Errorf("branch %q contains a character git does not allow", branch)
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("branch %q contains a control character", branch)
		}
	}
	return nil
}

// Primaries maps each configured repo name to its checkout on disk, skipping
// repos not found under any scan root.
func Primaries(cfg config.Config) map[string]string {
	out := map[string]string{}
	for name := range cfg.Repos {
		for _, root := range cfg.ScanRoots {
			p := filepath.Join(root, name)
			if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
				out[name] = p
				break
			}
		}
	}
	return out
}

// Find reports every configured repo holding branch. Repos are searched
// concurrently, bounded like aggregate.Collect's fan-out, since each match
// costs two git processes.
func Find(ctx context.Context, cfg config.Config, primaries map[string]string, branch string) []Match {
	var (
		mu      sync.Mutex
		matches []Match
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, runtime.NumCPU())
	for name, primary := range primaries {
		wg.Add(1)
		go func(name, primary string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, ok := findIn(ctx, cfg.Repos[name], name, primary, branch)
			if !ok {
				return
			}
			mu.Lock()
			matches = append(matches, m)
			mu.Unlock()
		}(name, primary)
	}
	wg.Wait()
	// Concurrency makes the order nondeterministic; a chooser and an error
	// message both need it stable.
	sortMatches(matches)
	return matches
}

func findIn(ctx context.Context, r config.Repo, name, primary, branch string) (Match, bool) {
	remotes := remotesOf(ctx, primary)
	// Complete refnames, matched literally. A "refs/remotes/*/<branch>" glob
	// would depend on for-each-ref's wildcard semantics for names containing
	// slashes, which is not worth relying on.
	patterns := []string{"refs/heads/" + branch}
	for _, rem := range remotes {
		patterns = append(patterns, "refs/remotes/"+rem+"/"+branch)
	}
	args := append([]string{"--no-optional-locks", "-C", primary,
		"for-each-ref", "--format=%(refname)"}, patterns...)
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return Match{}, false
	}

	m := Match{Repo: name, Primary: primary}
	found := false
	preferred := r.RemoteOrDefault()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch {
		case line == "":
			continue
		case line == "refs/heads/"+branch:
			m.Local, found = true, true
		case strings.HasPrefix(line, "refs/remotes/"):
			rem := strings.TrimSuffix(strings.TrimPrefix(line, "refs/remotes/"), "/"+branch)
			found = true
			// Prefer the configured remote when several carry the branch, so
			// the start point does not depend on ref iteration order.
			if m.Remote == "" || rem == preferred {
				m.Remote = rem
			}
		}
	}
	if !found {
		return Match{}, false
	}
	if ws, err := discover.Worktrees(ctx, primary, name); err == nil {
		for _, w := range ws {
			if w.Branch == branch {
				m.ExistingWorktree = w.Path
				break
			}
		}
	}
	return m, true
}

func remotesOf(ctx context.Context, primary string) []string {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary, "remote").Output()
	if err != nil {
		return nil
	}
	var res []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			res = append(res, l)
		}
	}
	return res
}

// Fetch updates every configured repo's remote refs. It runs only when a
// branch was not found anywhere, so its cost falls on the rare miss rather
// than on every invocation. Failures are ignored: a repo whose remote is
// unreachable should not stop the others being searched.
//
// Every remote is fetched, not just default_remote, because findIn searches
// every remote. Fetching only the default made the miss-then-fetch-then-retry
// path unable to find a branch that lives on a fork's `upstream` -- searchable
// but never fetched -- and report "not found ... (after fetching)", which is
// true and misleading.
//
// That is also why this no longer takes a config.Config: default_remote
// chooses a start point for a branch carried by several remotes, and has
// nothing to say about which remotes are worth refreshing.
func Fetch(ctx context.Context, primaries map[string]string) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for _, primary := range primaries {
		wg.Add(1)
		go func(primary string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Sequential within a repo: they contend on the same object store
			// and index lock, and the fan-out above already keeps every core
			// busy. One unreachable remote must not skip the rest, so nothing
			// here breaks out on error.
			for _, rem := range remotesOf(ctx, primary) {
				_ = exec.CommandContext(ctx, "git", "-C", primary, "fetch", "--prune", rem).Run()
			}
		}(primary)
	}
	wg.Wait()
}

func sortMatches(m []Match) {
	sort.Slice(m, func(i, j int) bool { return m[i].Repo < m[j].Repo })
}

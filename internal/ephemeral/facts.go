package ephemeral

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/model"
)

// prCacheTTL matches aggregate's PR cache lifetime, so a sweep right after
// a dashboard refresh costs no gh call.
const prCacheTTL = 10 * time.Minute

// gitOut runs a read-only git command in dir and returns trimmed stdout.
// --no-optional-locks matters beyond politeness: without it `git status`
// rewrites the index, and the index mtime is one of LastUsed's signals.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"--no-optional-locks", "-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	if err != nil {
		return "", withStderr(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// OwnCommits counts the commits on the worktree's HEAD that the repo's own
// user.email authored since wt created the worktree (spec §2.2).
//
// With a v2 marker the range is start..HEAD. A v1 marker has no start, so
// the range becomes "on HEAD but on no remote". An unset user.email drops
// the author filter, so every commit counts as yours: that keeps a worktree
// longer, which is the safe direction.
func OwnCommits(ctx context.Context, worktree string, m Marker) (int, error) {
	args := []string{"rev-list", "--count"}
	if email, err := gitOut(ctx, worktree, "config", "user.email"); err == nil && email != "" {
		// --fixed-strings: emails contain '.' and '+', which are regex
		// metacharacters. --regexp-ignore-case: email case is not identity,
		// and a miss here reads own work as someone else's. git honors -i
		// together with -F for --author (verified, git 2.50.1, 2026-10-15).
		args = append(args, "--fixed-strings", "--regexp-ignore-case", "--author=<"+email+">")
	}
	if m.Start != "" {
		args = append(args, m.Start+"..HEAD")
	} else {
		args = append(args, "HEAD", "--not", "--remotes")
	}
	out, err := gitOut(ctx, worktree, args...)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, fmt.Errorf("unreadable commit count %q: %w", out, err)
	}
	return n, nil
}

// openedAt is the later of when wt created the worktree and when it last
// handed it to the user.
func openedAt(m Marker) time.Time {
	if m.LastOpenedAt.After(m.CreatedAt) {
		return m.LastOpenedAt
	}
	return m.CreatedAt
}

// LastUsed is the latest sign of use (spec §2.3). It is floored at
// openedAt, so a worktree with no transcript and an untouched index reads
// as "used when created", never as the zero time -- which would make idle
// infinite and reap a committed-to worktree on sight.
func LastUsed(ctx context.Context, worktree string, m Marker, sessions []model.Session) time.Time {
	t := openedAt(m)
	for _, s := range sessions {
		if s.Mtime.After(t) {
			t = s.Mtime
		}
	}
	for _, name := range []string{"index", "logs/HEAD"} {
		p, err := gitOut(ctx, worktree, "rev-parse", "--git-path", name)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(worktree, p)
		}
		if st, err := os.Stat(p); err == nil && st.ModTime().After(t) {
			t = st.ModTime()
		}
	}
	return t
}

// prStateLookup is swapped out in tests (export_test.go).
var prStateLookup = lookupPRState

// PRState returns the state of the worktree's PR, or "" when there is none
// or it cannot be read. Failure is deliberately silent: an unknown state
// falls through to the idle rule, and a missing or logged-out gh must
// neither block every reap nor ever read as MERGED.
func PRState(ctx context.Context, primary string, m Marker) string {
	s, err := prStateLookup(ctx, primary, m)
	if err != nil {
		return ""
	}
	return s
}

func lookupPRState(ctx context.Context, primary string, m Marker) (string, error) {
	if m.PR != 0 {
		// gh pr list is keyed by headRefName, which collides across forks,
		// so a known PR number is asked about directly.
		cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(m.PR), "--json", "state")
		cmd.Dir = primary
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		var v struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return "", err
		}
		return v.State, nil
	}
	prs, ok := discover.CachedPRs(primary, prCacheTTL)
	if !ok {
		fresh, err := discover.PRsForRepo(ctx, primary)
		if err != nil {
			return "", err
		}
		_ = discover.WritePRCache(primary, fresh)
		prs = fresh
	}
	return prs[m.Branch].State, nil
}

// isLocked reports whether git considers worktree locked. `git worktree
// list --porcelain` prints a bare "locked" line (or "locked <reason>") in
// the worktree's block; verified against git 2.50 on 2026-10-15.
func isLocked(ctx context.Context, primary, worktree string) (bool, error) {
	out, err := gitOut(ctx, primary, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, block := range strings.Split(out, "\n\n") {
		var path string
		locked := false
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				path = strings.TrimPrefix(line, "worktree ")
			case line == "locked" || strings.HasPrefix(line, "locked "):
				locked = true
			}
		}
		if path != "" && sameDir(path, worktree) {
			return locked, nil
		}
	}
	return false, fmt.Errorf("%s is not in git worktree list", worktree)
}

// hasSubmodules reports whether any submodule is initialized in worktree.
// `git submodule status` prefixes an uninitialized one with '-'.
func hasSubmodules(ctx context.Context, worktree string) (bool, error) {
	out, err := gitOut(ctx, worktree, "submodule", "status", "--recursive")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, "-") {
			return true, nil
		}
	}
	return false, nil
}

// submodulesDirty runs the same status check inside every initialized
// submodule, recursively (spec §2.1). The superproject's own status already
// reports " M sub" for untracked content in a submodule (verified), so this
// is a second, independent layer -- the one --force removal leans on.
// --untracked-files=all for the same reason as dirtyCount.
func submodulesDirty(ctx context.Context, worktree string) (bool, error) {
	out, err := gitOut(ctx, worktree, "submodule", "foreach", "--quiet", "--recursive",
		"git status --porcelain --ignore-submodules=none --untracked-files=all")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// submoduleUnpushedScript prints, for one submodule, how many commits
// reachable from any of its refs -- branches, tags, refs/stash -- or from its
// HEAD exist on no remote-tracking ref. --all covers HEAD as well, so a
// detached commit and a tag-only commit are both counted. The && makes a
// failing rev-list fail the whole foreach, so an unreadable count is an
// error rather than an empty line.
const submoduleUnpushedScript = `n=$(git rev-list --count --all --not --remotes) && echo "$n"`

// submodulesUnpushed reports whether any initialized submodule, recursively,
// holds a commit that exists on no remote. In a linked worktree a
// submodule's git dir lives under .git/worktrees/<x>/modules/<sub>, so
// `git worktree remove --force` deletes the only copy of such a commit even
// though both status checks read clean (reproduced, git 2.50.1).
func submodulesUnpushed(ctx context.Context, worktree string) (bool, error) {
	out, err := gitOut(ctx, worktree, "submodule", "foreach", "--quiet", "--recursive",
		submoduleUnpushedScript)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return false, fmt.Errorf("unreadable submodule commit count %q: %w", line, err)
		}
		if n != 0 {
			return true, nil
		}
	}
	return false, nil
}

// unpushedCount counts commits on branch that exist on no remote-tracking
// ref. Fork PR heads are fetched to refs/remotes/wt-pr/<N>, so they count
// as "on a remote" here too.
func unpushedCount(ctx context.Context, primary, branch string) (int, error) {
	out, err := gitOut(ctx, primary, "rev-list", "--count", "refs/heads/"+branch, "--not", "--remotes")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

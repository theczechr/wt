package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theczechr/ephemera/internal/bootstrap"
	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/ephemeral"
	"github.com/theczechr/ephemera/internal/model"
	"github.com/theczechr/ephemera/internal/resolve"
	"github.com/theczechr/ephemera/internal/ui"
)

type prRef struct {
	Owner, Name string
	Number      int
}

var (
	prNumberArg = regexp.MustCompile(`^#?([0-9]+)$`)
	prURLArg    = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/([0-9]+)(?:[/?#].*)?$`)
)

// parsePRArg recognises `1234`, `#1234` and a github.com PR URL (spec
// §5.1). A bare number is always a PR; `wt open 1234` reaches a branch.
func parsePRArg(arg string) (prRef, bool) {
	if m := prNumberArg.FindStringSubmatch(arg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return prRef{Number: n}, n > 0
	}
	if m := prURLArg.FindStringSubmatch(arg); m != nil {
		n, _ := strconv.Atoi(m[3])
		return prRef{Owner: m[1], Name: m[2], Number: n}, n > 0
	}
	return prRef{}, false
}

type ghPR struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	HeadRefName       string `json:"headRefName"`
	HeadRefOid        string `json:"headRefOid"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	URL               string `json:"url"`
}

// usageError makes openPRAndCd exit 2 instead of 1.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// ghPRView asks gh about PR n in the repo checked out at primary (spec §5.2).
func ghPRView(ctx context.Context, primary string, n int) (ghPR, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return ghPR{}, errors.New("opening a PR needs the GitHub CLI (gh) on PATH: https://cli.github.com")
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(n), "--json",
		"number,state,headRefName,headRefOid,isCrossRepository,headRepositoryOwner,url")
	cmd.Dir = primary
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return ghPR{}, fmt.Errorf("gh pr view %d: %w: %s", n, err, bytes.TrimSpace(stderr.Bytes()))
	}
	var p ghPR
	if err := json.Unmarshal(out, &p); err != nil || p.HeadRefName == "" || p.HeadRefOid == "" {
		return ghPR{}, fmt.Errorf("gh pr view %d: unexpected output %q", n, bytes.TrimSpace(out))
	}
	return p, nil
}

// originMatches reports whether remoteURL points at github.com/<owner>/<repo>,
// in SSH or HTTPS form, ignoring case, a trailing .git and a trailing slash.
func originMatches(remoteURL, owner, repo string) bool {
	u := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(remoteURL), "/"), ".git"))
	want := strings.ToLower(owner + "/" + repo)
	return strings.HasSuffix(u, ":"+want) || strings.HasSuffix(u, "/"+want)
}

// repoForPR picks the configured repo a PR belongs to (spec §5.1).
func repoForPR(cfg config.Config, ref prRef, cwd string) (string, string, error) {
	primaries := resolve.Primaries(cfg)
	if ref.Owner != "" {
		for name, primary := range primaries {
			out, err := exec.Command("git", "-C", primary, "remote", "get-url",
				cfg.Repos[name].RemoteOrDefault()).Output()
			if err == nil && originMatches(string(out), ref.Owner, ref.Name) {
				return name, primary, nil
			}
		}
		return "", "", fmt.Errorf("no configured repo has %s/%s as its remote", ref.Owner, ref.Name)
	}
	if name, primary, err := resolveRepo(cfg, cwd); err == nil {
		return name, primary, nil
	}
	names := repoNames(primaries)
	if len(names) == 0 {
		return "", "", fmt.Errorf("no configured repo found under any scan root %v", cfg.ScanRoots)
	}
	if !ui.Interactive() {
		return "", "", usageError{fmt.Sprintf(
			"PR #%d: run this inside a repo, or pass the PR URL; configured repos: %s",
			ref.Number, strings.Join(names, ", "))}
	}
	idx, err := ui.Choose(fmt.Sprintf("which repo is PR #%d in?", ref.Number), names)
	if err != nil {
		return "", "", err
	}
	if idx < 0 {
		return "", "", errors.New("cancelled")
	}
	return names[idx], primaries[names[idx]], nil
}

// worktreeFor returns the worktree that has branch checked out, or "".
func worktreeFor(ctx context.Context, primary, branch string) string {
	ws, err := discover.Worktrees(ctx, primary, "")
	if err != nil {
		return ""
	}
	for _, w := range ws {
		if w.Branch == branch {
			return w.Path
		}
	}
	return ""
}

// fetchPR fetches PR head into refs/remotes/wt-pr/<n> (see model.PRRemoteRef).
func fetchPR(ctx context.Context, primary, remote string, n int) error {
	spec := fmt.Sprintf("+refs/pull/%d/head:%s", n, model.PRRemoteRef(n))
	out, err := exec.CommandContext(ctx, "git", "-C", primary, "fetch", "-q", remote, spec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetching PR #%d: %w: %s", n, err, bytes.TrimSpace(out))
	}
	return nil
}

// openPR resolves a PR to a worktree path, creating or updating it.
func openPR(cfg config.Config, ref prRef, cwd string, stderr io.Writer) (string, error) {
	repoName, primary, err := repoForPR(cfg, ref, cwd)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
	defer cancel()
	pr, err := ghPRView(ctx, primary, ref.Number)
	if err != nil {
		return "", err
	}
	r := cfg.Repos[repoName]
	remote := r.RemoteOrDefault()
	branch := pr.HeadRefName
	if pr.IsCrossRepository {
		branch = fmt.Sprintf("pr/%d", pr.Number)
	}

	if existing := worktreeFor(ctx, primary, branch); existing != "" {
		updateToPRHead(ctx, existing, primary, remote, pr, stderr)
		return existing, nil
	}

	if !pr.IsCrossRepository {
		path, err := openBranchIn(cfg, map[string]string{repoName: primary}, branch)
		if err != nil {
			return "", err
		}
		// Only an ephemeral has a marker; a permanent worktree stays as is.
		_ = ephemeral.UpdateMarker(path, func(m *ephemeral.Marker) { m.PR = pr.Number })
		return path, nil
	}

	if !r.EphemeralEnabled() {
		return "", fmt.Errorf("repo %q has ephemeral = false; fork PRs open only as ephemerals", repoName)
	}
	if err := fetchPR(ctx, primary, remote, pr.Number); err != nil {
		return "", err
	}
	created := false
	if !bootstrapBranchExists(ctx, primary, branch) {
		if out, err := exec.CommandContext(ctx, "git", "-C", primary, "branch", branch,
			model.PRRemoteRef(pr.Number)).CombinedOutput(); err != nil {
			return "", fmt.Errorf("creating %s: %w: %s", branch, err, bytes.TrimSpace(out))
		}
		created = true
	}
	target := filepath.Join(primary, r.EphemeralDirOrDefault(), bootstrap.Slug(branch))
	if err := ephemeral.EnsureExcluded(ctx, primary,
		[]string{r.EphemeralDirOrDefault() + "/", ephemeral.MarkerName}); err != nil {
		return "", err
	}
	if err := bootstrap.CreateAt(ctx, primary, target, branch, "", r.ForEphemeral()); err != nil {
		return "", fmt.Errorf("bootstrapping %s: %w (left in place for you to inspect or remove)", target, err)
	}
	start, err := headOf(ctx, target)
	if err != nil {
		return "", fmt.Errorf("created %s but %w", target, err)
	}
	if err := ephemeral.WriteMarker(target, ephemeral.Marker{
		Repo: repoName, Branch: branch, Primary: primary, CreatedAt: time.Now(),
		Start: start, PR: pr.Number, BranchCreatedByWT: created,
	}); err != nil {
		return "", fmt.Errorf("created %s but could not mark it ephemeral: %w", target, err)
	}
	return target, nil
}

// updateToPRHead brings an existing PR worktree to the PR's current head
// when that loses nothing: clean, and no commits of the user's own (spec
// §5.3). Otherwise it says how far behind the worktree is and leaves it.
func updateToPRHead(ctx context.Context, wt, primary, remote string, pr ghPR, stderr io.Writer) {
	if pr.IsCrossRepository {
		if err := fetchPR(ctx, primary, remote, pr.Number); err != nil {
			fmt.Fprintln(stderr, "wt:", err)
			return
		}
	} else {
		_ = exec.CommandContext(ctx, "git", "-C", primary, "fetch", "-q", remote, pr.HeadRefName).Run()
	}
	head, err := headOf(ctx, wt)
	if err != nil || head == pr.HeadRefOid {
		return
	}
	status, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", wt, "status", "--porcelain").Output()
	own := 1
	if m, merr := ephemeral.ReadMarker(wt); merr == nil {
		if n, oerr := ephemeral.OwnCommits(ctx, wt, m); oerr == nil {
			own = n
		}
	}
	if err == nil && len(bytes.TrimSpace(status)) == 0 && own == 0 {
		if out, err := exec.CommandContext(ctx, "git", "-C", wt, "merge", "--ff-only", "-q", pr.HeadRefOid).CombinedOutput(); err != nil {
			fmt.Fprintf(stderr, "wt: could not fast-forward to the PR head: %s\n", bytes.TrimSpace(out))
		}
		return
	}
	n := "?"
	if out, err := exec.CommandContext(ctx, "git", "-C", wt, "rev-list", "--count", "HEAD.."+pr.HeadRefOid).Output(); err == nil {
		n = strings.TrimSpace(string(out))
	}
	plural := "s"
	if n == "1" {
		plural = ""
	}
	fmt.Fprintf(stderr, "wt: PR #%d has %s new commit%s; your worktree has local work, so it was left as is\n",
		pr.Number, n, plural)
}

// openPRAndCd is the CLI entry for `wt <PR>`.
func openPRAndCd(cfg config.Config, ref prRef) {
	cwd, _ := os.Getwd()
	path, err := openPR(cfg, ref, cwd, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		var ue usageError
		if errors.As(err, &ue) {
			os.Exit(2)
		}
		os.Exit(1)
	}
	handOff(path, "")
}

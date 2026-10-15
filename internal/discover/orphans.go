package discover

import (
	"context"
	"os/exec"
	"strings"
)

// UnpushedOrphans lists local branches that hold commits on no remote and
// are not checked out in any worktree (spec §2.5). This is what keeps the
// reaper's "the branch survives" promise honest: work that survives only on
// a branch nobody looks at is still visible somewhere.
func UnpushedOrphans(ctx context.Context, primary string) ([]string, error) {
	ws, err := Worktrees(ctx, primary, "")
	if err != nil {
		return nil, err
	}
	checkedOut := map[string]bool{}
	for _, w := range ws {
		checkedOut[w.Branch] = true
	}
	// lstrip=2, not :short -- a tag sharing a branch's name turns the short
	// form into "heads/<name>", which matches no worktree and no ref below.
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary,
		"for-each-ref", "--format=%(refname:lstrip=2)", "refs/heads/").Output()
	if err != nil {
		return nil, err
	}
	var res []string
	for _, b := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if b == "" || checkedOut[b] {
			continue
		}
		one, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", primary,
			"rev-list", "-n", "1", "refs/heads/"+b, "--not", "--remotes").Output()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(one)) != "" {
			res = append(res, b)
		}
	}
	return res, nil
}

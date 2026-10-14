package main

import (
	"context"
	"fmt"
	"io"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/discover"
	"github.com/theczechr/ephemera/internal/resolve"
)

// runBranches implements `wt branches --unpushed`: one "repo<TAB>branch"
// line per local branch with commits on no remote and no worktree.
func runBranches(cfg config.Config, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--unpushed" {
		fmt.Fprintln(stderr, "usage: wt branches --unpushed")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	primaries := resolve.Primaries(cfg)
	for _, name := range repoNames(primaries) {
		bs, err := discover.UnpushedOrphans(ctx, primaries[name])
		if err != nil {
			fmt.Fprintf(stderr, "wt: branches: %s: %v\n", name, err)
			return 1
		}
		for _, b := range bs {
			fmt.Fprintf(stdout, "%s\t%s\n", name, b)
		}
	}
	return 0
}

// countOrphans feeds the dashboard footer. Errors count as zero: it is a
// hint, and a failed probe must not paint a warning that isn't true.
func countOrphans(cfg config.Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	n := 0
	for _, primary := range resolve.Primaries(cfg) {
		if bs, err := discover.UnpushedOrphans(ctx, primary); err == nil {
			n += len(bs)
		}
	}
	return n
}

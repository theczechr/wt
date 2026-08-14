package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/theczechr/wt/internal/model"
)

// syntheticWorkspaces approximates the real fleet this was measured against:
// 71 worktrees across 3 repos, a couple of sessions each, some dirty, some
// with a PR.
func syntheticWorkspaces(n int) []model.Workspace {
	repos := []string{"backend", "frontend", "dashboard"}
	ws := make([]model.Workspace, n)
	for i := range ws {
		ws[i] = model.Workspace{
			Repo:       repos[i%len(repos)],
			Path:       fmt.Sprintf("/Users/alice/code/%s/.worktrees/branch-%d", repos[i%len(repos)], i),
			Branch:     fmt.Sprintf("feature/some-branch-name-%d", i),
			DirtyCount: i % 5,
			PR:         model.PR{Number: 1000 + i, State: "OPEN"},
			Kind:       model.KindNested,
			Sessions: []model.Session{
				{ID: fmt.Sprintf("s%d-a", i), Title: "Some AI-generated session title", Mtime: time.Now()},
				{ID: fmt.Sprintf("s%d-b", i), LastPrompt: "fix the flaky test that keeps failing on CI", Mtime: time.Now()},
			},
		}
	}
	return ws
}

// BenchmarkViewSeventyOneWorkspaces measures the pure render cost of the
// dashboard's View() against a fleet the size of the one this optimisation
// was measured against (71 worktrees, 3 repos) -- the part of "time to
// first frame" that isn't data loading, and that WT_DEBUG_TIMING (see
// cmd/wt/main.go) doesn't cover since it can't run without a real TTY.
func BenchmarkViewSeventyOneWorkspaces(b *testing.B) {
	m := uiModel{ws: sortWorkspaces(syntheticWorkspaces(71)), width: 160, height: 40}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

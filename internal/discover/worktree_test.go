package discover

import (
	"testing"

	"github.com/theczechr/wt/internal/model"
)

const porcelain = `worktree /Users/alice/code/server
HEAD a1b2c3d4e5f60718293a4b5c6d7e8f901234567a
branch refs/heads/feature/refund-payment-queue

worktree /Users/alice/.sprout/worktrees/server/sprout-11ab65f9
HEAD 0123456789abcdef0123456789abcdef0123456f
branch refs/heads/sprout/fix/admin-report-export

worktree /Users/alice/code/server-cache
HEAD 565f9962a0000000000000000000000000000000
branch refs/heads/feature/checkout-progressive-card-fields

worktree /Users/alice/code/server/.worktrees/queue-lock
HEAD 27af8c5bd0000000000000000000000000000000
branch refs/heads/fix/account-balance-advisory-lock

worktree /Users/alice/code/server/.claude/worktrees/pr-4004
HEAD 8c8e4b6450000000000000000000000000000000
branch refs/heads/fix/session-limits-multi-id-pushdown

worktree /Users/alice/code/server/.worktrees/csv-import-backfill
HEAD 47dac29640000000000000000000000000000000
detached
`

func TestParseWorktreeListClassifiesKinds(t *testing.T) {
	primary := "/Users/alice/code/server"
	got := ParseWorktreeList(porcelain, "backend", primary)
	if len(got) != 6 {
		t.Fatalf("got %d worktrees, want 6", len(got))
	}

	want := []struct {
		path   string
		branch string
		kind   model.Kind
	}{
		{primary, "feature/refund-payment-queue", model.KindPrimary},
		{"/Users/alice/.sprout/worktrees/server/sprout-11ab65f9", "sprout/fix/admin-report-export", model.KindForeign},
		{"/Users/alice/code/server-cache", "feature/checkout-progressive-card-fields", model.KindSibling},
		{primary + "/.worktrees/queue-lock", "fix/account-balance-advisory-lock", model.KindNested},
		{primary + "/.claude/worktrees/pr-4004", "fix/session-limits-multi-id-pushdown", model.KindClaudeManaged},
		{primary + "/.worktrees/csv-import-backfill", "", model.KindNested},
	}
	for i, w := range want {
		if got[i].Path != w.path {
			t.Errorf("[%d] Path = %q, want %q", i, got[i].Path, w.path)
		}
		if got[i].Branch != w.branch {
			t.Errorf("[%d] Branch = %q, want %q", i, got[i].Branch, w.branch)
		}
		if got[i].Kind != w.kind {
			t.Errorf("[%d] Kind = %q, want %q", i, got[i].Kind, w.kind)
		}
		if got[i].Repo != "backend" {
			t.Errorf("[%d] Repo = %q, want server", i, got[i].Repo)
		}
	}
	if got[0].Head != "a1b2c3d4e5f60718293a4b5c6d7e8f901234567a" {
		t.Errorf("Head = %q", got[0].Head)
	}
}

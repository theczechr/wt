package model

import "testing"

func TestProjectDirNameFlattensSlashAndDot(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"/Users/alice/code/server",
			"-Users-alice-code-server",
		},
		{
			"/Users/alice/code/server/.worktrees/user-tree-savepoint",
			"-Users-alice-code-server--worktrees-user-tree-savepoint",
		},
		{
			"/Users/alice/code/server/.claude/worktrees/pr-4004",
			"-Users-alice-code-server--claude-worktrees-pr-4004",
		},
		{
			"/Users/alice/.sprout/worktrees/server/sprout-11ab65f9",
			"-Users-alice--sprout-worktrees-server-sprout-11ab65f9",
		},
	}
	for _, c := range cases {
		if got := ProjectDirName(c.in); got != c.want {
			t.Errorf("ProjectDirName(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

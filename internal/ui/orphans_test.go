package ui

import (
	"strings"
	"testing"
)

func TestFooterShowsUnpushedOrphanCount(t *testing.T) {
	m := withSessions(1)
	m.orphanCount = 3
	if v := m.View(); !strings.Contains(v, "3 local branches with unpushed commits and no worktree") {
		t.Errorf("footer does not show the count:\n%s", v)
	}
	m.orphanCount = 0
	if v := m.View(); strings.Contains(v, "unpushed commits and no worktree") {
		t.Error("no note when there are none")
	}
}

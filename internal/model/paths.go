package model

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectDirName converts an absolute worktree path into the directory name
// Claude Code uses under ~/.claude/projects. Both '/' and '.' become '-'.
//
// The mapping is lossy and MUST only be applied in this direction: flatten a
// known path and look the result up. Never parse a directory name back into a
// path.
func ProjectDirName(absPath string) string {
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(absPath)
}

// ClaudeProjectsDir returns ~/.claude/projects.
func ClaudeProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

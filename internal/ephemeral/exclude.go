package ephemeral

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureExcluded appends each line to the repository's info/exclude, skipping
// any already present. That file is local to the clone and never committed,
// so this is the one way to hide wt's artefacts without touching a tracked
// .gitignore the user shares with their team.
//
// repoPath may be the primary checkout or any worktree of it: the target is
// resolved through --git-common-dir, since a worktree's own .git is a file
// and has no info/ directory of its own.
func EnsureExcluded(ctx context.Context, repoPath string, lines []string) error {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", repoPath,
		"rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return fmt.Errorf("resolving git common dir for %s: %w", repoPath, err)
	}
	infoDir := filepath.Join(strings.TrimSpace(string(out)), "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return err
	}
	excludePath := filepath.Join(infoDir, "exclude")

	present := map[string]bool{}
	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(existing)))
	for scanner.Scan() {
		present[strings.TrimSpace(scanner.Text())] = true
	}

	var add strings.Builder
	for _, l := range lines {
		if present[l] {
			continue
		}
		add.WriteString(l)
		add.WriteString("\n")
		present[l] = true
	}
	if add.Len() == 0 {
		return nil
	}

	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return os.WriteFile(excludePath, []byte(body+add.String()), 0o644)
}

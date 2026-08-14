package ephemeral

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SessionEnd is the payload Claude Code writes to a SessionEnd hook's stdin.
// Field names verified against the hook documentation on 2026-08-13.
type SessionEnd struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	Reason        string `json:"reason"`
	HookEventName string `json:"hook_event_name"`
}

// ParseSessionEnd decodes the hook payload. A payload that cannot be decoded
// is an error rather than a zero value: a zero Cwd would otherwise resolve to
// the process's own directory and put an unrelated worktree up for removal.
func ParseSessionEnd(r io.Reader) (SessionEnd, error) {
	var s SessionEnd
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return s, fmt.Errorf("decoding SessionEnd payload: %w", err)
	}
	if s.Cwd == "" {
		// {} and {"reason":"other"} decode cleanly, leaving Cwd == "". That is
		// worse than a decode failure, not equivalent to one: `git -C ""`
		// silently means "use my own working directory", so an empty Cwd
		// would resolve to the wt process's own cwd rather than erroring --
		// exactly the "put an unrelated worktree up for removal" case above,
		// reached through valid JSON instead of invalid.
		return s, fmt.Errorf("SessionEnd payload has no cwd")
	}
	return s, nil
}

// ShouldReap reports whether a SessionEnd with this reason means the session
// is really over.
//
// This is a deny-list, not an allow-list, and the asymmetry is deliberate.
// "clear" and "resume" are documented to fire while the user is still working
// in that directory. Every other reason -- including the catch-all "other" a
// normal quit most plausibly arrives as, and any reason added in a future
// release -- means the session is done. An allow-list would fail closed so
// thoroughly that the feature would appear not to work at all; the reap
// predicate is what makes failing open here safe.
func ShouldReap(reason string) bool {
	switch reason {
	case "clear", "resume":
		return false
	}
	return true
}

// WorktreeRoot resolves dir to the root of the worktree containing it. A
// SessionEnd's cwd is Claude's working directory, which may be nested inside
// the worktree rather than at its root.
func WorktreeRoot(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", dir,
		"rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git worktree: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// LogReap appends one line to the reap log. A SessionEnd hook's stderr
// reaches only the session transcript and its exit code is ignored, so
// without this a refusal -- the interesting case -- would leave no trace
// anywhere the user looks.
func LogReap(worktree string, err error) {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		return
	}
	dir := filepath.Join(home, ".local", "state", "wt")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "reap.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer f.Close()
	outcome := "reaped"
	if err != nil {
		outcome = err.Error()
	}
	fmt.Fprintf(f, "%s\t%s\t%s\n", time.Now().Format(time.RFC3339), worktree, outcome)
}

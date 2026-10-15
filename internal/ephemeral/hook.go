package ephemeral

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
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

package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// InPluginPane reports whether this process is a herdr plugin pane
// entrypoint, i.e. whether the dashboard was opened by herdr rather than run
// from a shell.
//
// The distinction decides how a chosen worktree is acted on, and it is not
// the same question as "is herdr running" or even "is this pane inside
// herdr". wt normally hands its choice to the zsh wrapper through
// $TMPDIR/wt-cd, because a process cannot change its parent shell's working
// directory. Run from a shell -- inside a herdr pane or outside one -- that
// is still exactly right, and the wrapper is still there to act on it.
//
// A plugin pane has no shell parent at all: herdr launches the command
// directly and closes the overlay when it exits. The file would be written
// and nobody would ever read it, so the action becomes a silent no-op. There
// the choice has to be handed back to herdr instead.
//
// HERDR_PLUGIN_ENTRYPOINT_ID is the precise signal: herdr documents it as
// set for pane commands specifically, unlike HERDR_ENV or HERDR_PANE_ID
// which are broader and would misfire on a shell-launched wt.
func InPluginPane() bool {
	return os.Getenv("HERDR_PLUGIN_ENTRYPOINT_ID") != ""
}

// bin is the herdr executable. Herdr injects HERDR_BIN_PATH into every
// runtime command precisely so a plugin need not locate it, which also keeps
// this working when herdr is not on PATH; the bare name is only a fallback.
func bin() string {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p
	}
	return "herdr"
}

// OpenedWorktree is what wt needs from a worktree.open response.
type OpenedWorktree struct {
	PaneID      string
	WorkspaceID string
	// AlreadyOpen reports that a workspace was already showing this
	// worktree, so opening it only focused what was there.
	AlreadyOpen bool
}

// cliEnvelope is herdr's CLI response line: one JSON object carrying either
// result or error.
type cliEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// decode finds the first line of herdr CLI output that is a response
// envelope. The CLI speaks newline-delimited JSON and may print more than
// one line, so this scans rather than assuming a single object.
func decode(out []byte) (json.RawMessage, error) {
	var lastErr error
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var env cliEnvelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			lastErr = err
			continue
		}
		if env.Error != nil {
			return nil, fmt.Errorf("herdr: %s: %s", env.Error.Code, env.Error.Message)
		}
		if len(env.Result) > 0 {
			return env.Result, nil
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("could not parse herdr response: %w", lastErr)
	}
	return nil, fmt.Errorf("herdr returned no response object")
}

// OpenWorktree opens the worktree at path in herdr and focuses it. It is
// idempotent: herdr reports already_open rather than creating a duplicate
// workspace when one is already showing that worktree.
func OpenWorktree(ctx context.Context, path string) (OpenedWorktree, error) {
	out, err := exec.CommandContext(ctx, bin(),
		"worktree", "open", "--path", path, "--focus").CombinedOutput()
	if err != nil {
		// Prefer herdr's own structured error over the exit status, which
		// says nothing useful.
		if _, derr := decode(out); derr != nil {
			return OpenedWorktree{}, derr
		}
		return OpenedWorktree{}, fmt.Errorf("herdr worktree open: %w", err)
	}
	res, err := decode(out)
	if err != nil {
		return OpenedWorktree{}, err
	}
	var opened struct {
		AlreadyOpen bool `json:"already_open"`
		RootPane    struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(res, &opened); err != nil {
		return OpenedWorktree{}, fmt.Errorf("could not read herdr worktree.open result: %w", err)
	}
	return OpenedWorktree{
		PaneID:      opened.RootPane.PaneID,
		WorkspaceID: opened.Workspace.WorkspaceID,
		AlreadyOpen: opened.AlreadyOpen,
	}, nil
}

// ResumeClaude starts `claude --resume <sessionID>` in an existing pane.
//
// herdr requires that pane to be sitting at an interactive shell prompt, so
// this is only correct for a pane herdr just created. A worktree that was
// already open is deliberately left alone by the caller: whatever is running
// there is more likely to be the session the user wanted than a second copy
// started underneath it.
func ResumeClaude(ctx context.Context, paneID, sessionID, label string) error {
	if paneID == "" {
		return fmt.Errorf("no pane to resume in")
	}
	out, err := exec.CommandContext(ctx, bin(),
		"agent", "start", label, "--kind", "claude", "--pane", paneID,
		"--", "--resume", sessionID).CombinedOutput()
	if err != nil {
		if _, derr := decode(out); derr != nil {
			return derr
		}
		return fmt.Errorf("herdr agent start: %w", err)
	}
	return nil
}

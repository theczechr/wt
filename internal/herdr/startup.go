package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Session is the part of herdr's live snapshot that decides whether the
// dashboard is worth opening unprompted.
type Session struct {
	Agents int
	Panes  int
}

// Busy reports whether herdr already has something worth looking at.
//
// The test is "is an agent running", not "is a pane open". herdr always has
// at least one pane -- an empty herdr is still a terminal -- so a pane count
// says nothing. An agent is the unit of work herdr exists to manage, so no
// agents means nothing is in flight and a launcher is welcome; any agent
// means the user came back to work already underway, and an overlay dropped
// over it is an interruption.
func (s Session) Busy() bool { return s.Agents > 0 }

// Snapshot reads herdr's live session state.
func Snapshot(ctx context.Context) (Session, error) {
	out, err := exec.CommandContext(ctx, bin(), "api", "snapshot").CombinedOutput()
	if err != nil {
		if _, derr := decode(out); derr != nil {
			return Session{}, derr
		}
		return Session{}, fmt.Errorf("herdr api snapshot: %w", err)
	}
	res, err := decode(out)
	if err != nil {
		return Session{}, err
	}
	// The snapshot arrives boxed under "snapshot"; tolerate it being
	// inlined, since that nesting is an implementation detail of the
	// response type rather than a documented guarantee.
	var boxed struct {
		Snapshot *struct {
			Agents []json.RawMessage `json:"agents"`
			Panes  []json.RawMessage `json:"panes"`
		} `json:"snapshot"`
		Agents []json.RawMessage `json:"agents"`
		Panes  []json.RawMessage `json:"panes"`
	}
	if err := json.Unmarshal(res, &boxed); err != nil {
		return Session{}, fmt.Errorf("could not read herdr snapshot: %w", err)
	}
	if boxed.Snapshot != nil {
		return Session{Agents: len(boxed.Snapshot.Agents), Panes: len(boxed.Snapshot.Panes)}, nil
	}
	return Session{Agents: len(boxed.Agents), Panes: len(boxed.Panes)}, nil
}

// OpenDashboard opens wt's plugin pane. Placement is left to the manifest so
// there is one place that decides how the dashboard appears.
func OpenDashboard(ctx context.Context, pluginID, entrypoint string) error {
	out, err := exec.CommandContext(ctx, bin(),
		"plugin", "pane", "open", "--plugin", pluginID, "--entrypoint", entrypoint).CombinedOutput()
	if err != nil {
		if _, derr := decode(out); derr != nil {
			return derr
		}
		return fmt.Errorf("herdr plugin pane open: %w", err)
	}
	return nil
}

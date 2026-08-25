package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Agent status values herdr reports. Confirmed against its generated API
// schema rather than observed: AgentStatus is an enum of exactly these.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusUnknown = "unknown"
)

// Agent is one agent herdr is running, reduced to what wt joins on.
type Agent struct {
	// Cwd is the pane's working directory -- the directory the pane was
	// launched in.
	//
	// Deliberately NOT foreground_cwd, which herdr also reports. That field
	// follows the pane's foreground process, and for a Claude pane it
	// routinely points somewhere else entirely: on this machine a pane
	// launched in a server worktree reported a foreground_cwd inside an
	// unrelated MCP server's directory. Joining on it would attribute an
	// agent to the wrong worktree, or to none.
	Cwd     string
	Status  string
	Kind    string // "claude", "codex", ...
	Session string // agent_session.value when kind == "id"; the Claude session UUID
}

// Index maps worktree paths to the agents running in them.
type Index struct {
	byCwd map[string][]Agent
}

// ErrNotRunning reports that herdr is not running at all. It is a real
// answer, not a failure: wt works perfectly well without herdr, and every
// worktree genuinely has zero herdr agents in that case.
//
// It is kept distinct from every other error on purpose. This package's
// three-way rule (see worktree.go in discover) applies here too: absent is
// benign and may report "no agents", but unreadable is unknown and must not.
// A caller that feeds this into a deletion gate has to be able to tell them
// apart, so the discriminator below is positive evidence of absence -- the
// socket is missing, or the binary is not installed -- never a fallback for
// an error we failed to classify.
var ErrNotRunning = errors.New("herdr is not running")

// Running reports whether a herdr daemon appears to be up, by the presence
// of the socket it serves on. HERDR_SOCKET_PATH is injected into plugin
// runtime commands and set by `herdr --session`; the default path is the
// fallback.
func Running() bool {
	sock := os.Getenv("HERDR_SOCKET_PATH")
	if sock == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		sock = filepath.Join(home, ".config", "herdr", "herdr.sock")
	}
	_, err := os.Stat(sock)
	return err == nil
}

// Agents reads every agent herdr is running.
//
// It returns ErrNotRunning when no daemon is up, and a descriptive error
// when one is up but could not be read. Callers must not collapse the two.
func Agents(ctx context.Context) (Index, error) {
	if !Running() {
		return Index{}, ErrNotRunning
	}
	out, err := exec.CommandContext(ctx, bin(), "api", "snapshot").CombinedOutput()
	if err != nil {
		if _, derr := decode(out); derr != nil {
			return Index{}, derr
		}
		// The binary is missing rather than failing: herdr is genuinely not
		// installed here, which is absence, not an unreadable daemon.
		if errors.Is(err, exec.ErrNotFound) {
			return Index{}, ErrNotRunning
		}
		return Index{}, fmt.Errorf("herdr api snapshot: %w", err)
	}
	res, err := decode(out)
	if err != nil {
		return Index{}, err
	}

	type paneJSON struct {
		PaneID string `json:"pane_id"`
		Cwd    string `json:"cwd"`
		Agent  string `json:"agent"`
		Status string `json:"agent_status"`
		Sess   *struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"agent_session"`
	}
	var boxed struct {
		Snapshot *struct {
			Panes []paneJSON `json:"panes"`
		} `json:"snapshot"`
		Panes []paneJSON `json:"panes"`
	}
	if err := json.Unmarshal(res, &boxed); err != nil {
		return Index{}, fmt.Errorf("could not read herdr snapshot: %w", err)
	}
	panes := boxed.Panes
	if boxed.Snapshot != nil {
		panes = boxed.Snapshot.Panes
	}

	idx := Index{byCwd: map[string][]Agent{}}
	for _, p := range panes {
		// A pane with no agent is just a shell. herdr reports its
		// agent_status as "unknown", which must not be confused with an
		// agent whose state could not be determined.
		if p.Agent == "" || p.Cwd == "" {
			continue
		}
		a := Agent{Cwd: filepath.Clean(p.Cwd), Status: p.Status, Kind: p.Agent}
		if p.Sess != nil && p.Sess.Kind == "id" {
			a.Session = p.Sess.Value
		}
		idx.byCwd[a.Cwd] = append(idx.byCwd[a.Cwd], a)
	}
	return idx, nil
}

// All returns every agent herdr is running, in no particular order.
func (i Index) All() []Agent {
	var out []Agent
	for _, agents := range i.byCwd {
		out = append(out, agents...)
	}
	return out
}

// Contains reports whether an agent whose pane cwd is cwd belongs to the
// worktree at path.
//
// Containment, not equality: herdr records where a pane was launched, and
// nothing stops that being deeper in the tree than the worktree root.
// Comparison is on whole path segments, so ".../server-dqs" never swallows
// ".../server-dqsfix".
func Contains(path, cwd string) bool {
	path, cwd = filepath.Clean(path), filepath.Clean(cwd)
	return cwd == path || strings.HasPrefix(cwd, path+string(filepath.Separator))
}

// Attribute assigns each agent to exactly one of paths: the LONGEST path
// that contains its pane cwd.
//
// Longest wins because worktrees nest. A repo's nested worktrees live under
// the primary checkout -- .../server/.worktrees/x and
// .../server/.claude/worktrees/y are both inside .../server -- so plain
// containment matches an agent against its worktree AND against the primary,
// reporting the same agent twice in two different rows. Observed live: one
// Claude pane in .claude/worktrees/crm-v1-changes-api was attributed both
// there and to the primary server checkout.
//
// The most specific containing worktree is the one the agent is actually in,
// so ties cannot occur: two worktree paths of equal length cannot both
// contain the same cwd unless they are the same path.
func (i Index) Attribute(paths []string) map[string][]Agent {
	out := map[string][]Agent{}
	for _, a := range i.All() {
		best := ""
		for _, p := range paths {
			if Contains(p, a.Cwd) && len(p) > len(best) {
				best = p
			}
		}
		if best != "" {
			out[best] = append(out[best], a)
		}
	}
	return out
}

// Rank orders statuses by how much they want the user's attention. It is
// what collapses several agents in one worktree into a single row state.
func Rank(status string) int {
	switch status {
	case StatusBlocked:
		return 4 // waiting on a human -- the whole reason to look at a dashboard
	case StatusWorking:
		return 3
	case StatusIdle:
		return 2
	case StatusDone:
		return 1
	default:
		return 0
	}
}

// Worst returns the most attention-worthy status among agents.
func Worst(agents []Agent) string {
	best := ""
	for _, a := range agents {
		if Rank(a.Status) > Rank(best) {
			best = a.Status
		}
	}
	return best
}

// Package herdr parses the plugin event payloads herdr hands to wt.
//
// wt runs as a herdr plugin (see herdr-plugin.toml). Herdr creates git
// worktrees itself -- `worktree.create` takes branch, base and path -- but it
// has no bootstrap concept at all: no post-create step, no env handling, no
// submodule init. A worktree herdr makes of a repo that keeps its .env files
// gitignored therefore cannot build. wt already fixes exactly that, so the
// integration is one event hook: herdr announces worktree.created, wt
// bootstraps the path it names.
//
// Herdr passes the payload in HERDR_PLUGIN_EVENT_JSON. The event envelope is
//
//	{"event":"worktree.created",
//	 "data":{"type":"worktree_created",
//	         "workspace":{...},
//	         "worktree":{"path":"...","branch":"...",...}}}
//
// confirmed against herdr's generated API schema (schema_version 1, protocol
// 20) rather than inferred: EventEnvelope requires "event" and "data", and
// EventData's worktree_created variant requires "type", "workspace" and
// "worktree".
//
// The one thing the schema does NOT settle is whether herdr sets
// HERDR_PLUGIN_EVENT_JSON to the whole envelope or to the data object alone --
// that is decided in herdr's plugin runtime, not in the wire schema, and it
// was not verified at runtime. ParseWorktreeCreated therefore accepts both
// spellings. This is deliberately tolerant about SHAPE while staying strict
// about CONTENT: an envelope missing its worktree path is still an error, not
// a silent no-op, because a hook that quietly does nothing is
// indistinguishable from a hook that is not wired up.
package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// WorktreeInfo is the subset of herdr's WorktreeInfo wt needs. Herdr sends
// considerably more (is_bare, is_prunable, open_workspace_id, label...);
// unlisted fields are ignored by encoding/json, so herdr adding fields cannot
// break this parse.
type WorktreeInfo struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// envelope models both accepted spellings at once. Data is a pointer so a
// payload that omits it is distinguishable from one that carries an empty
// object.
type envelope struct {
	Event string `json:"event"`
	Data  *struct {
		Type     string       `json:"type"`
		Worktree WorktreeInfo `json:"worktree"`
	} `json:"data"`
	// Bare-data spelling: worktree sits at the top level.
	Type     string       `json:"type"`
	Worktree WorktreeInfo `json:"worktree"`
}

// ErrNotWorktreeCreated reports a payload that parsed cleanly but describes a
// different event. Callers treat it as "nothing to do", separately from a
// malformed payload, which is a wiring bug worth reporting.
var ErrNotWorktreeCreated = errors.New("payload is not a worktree.created event")

// ParseWorktreeCreated extracts the created worktree from a
// HERDR_PLUGIN_EVENT_JSON payload.
func ParseWorktreeCreated(payload string) (WorktreeInfo, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return WorktreeInfo{}, errors.New("HERDR_PLUGIN_EVENT_JSON is empty: the hook ran outside herdr, or herdr changed how it passes event payloads")
	}

	var e envelope
	if err := json.Unmarshal([]byte(payload), &e); err != nil {
		return WorktreeInfo{}, fmt.Errorf("HERDR_PLUGIN_EVENT_JSON is not valid JSON: %w", err)
	}

	kind, wt := e.Type, e.Worktree
	if e.Data != nil {
		kind, wt = e.Data.Type, e.Data.Worktree
	}
	// The envelope's own "event" field is the more reliable discriminator
	// when present, since it survives either spelling.
	if e.Event != "" {
		kind = e.Event
	}

	// Both spellings of the same event name are accepted: "worktree.created"
	// is EventKind's wire name, "worktree_created" is EventData's serde tag,
	// and which one lands here depends on the unverified question above.
	if kind != "" && kind != "worktree.created" && kind != "worktree_created" {
		return WorktreeInfo{}, fmt.Errorf("%w: got %q", ErrNotWorktreeCreated, kind)
	}

	if wt.Path == "" {
		return WorktreeInfo{}, errors.New("event carries no worktree path: nothing to bootstrap")
	}
	return wt, nil
}

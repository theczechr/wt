package herdr

import (
	"errors"
	"testing"
)

// envelopeShape is the payload spelling herdr's generated API schema
// describes: EventEnvelope{event, data}, with data being EventData's
// worktree_created variant. Extra fields are present on purpose -- herdr
// sends far more than wt reads, and adding more must never break the parse.
const envelopeShape = `{
  "event": "worktree.created",
  "data": {
    "type": "worktree_created",
    "workspace": {"workspace_id":"w1","number":1,"label":"server","focused":true,
                  "pane_count":1,"tab_count":1,"active_tab_id":"t1","agent_status":"idle",
                  "worktree":{"repo_key":"k","repo_name":"server","repo_root":"/r",
                              "checkout_path":"/r/.worktrees/x","is_linked_worktree":true}},
    "worktree": {"path":"/r/.worktrees/x","branch":"feature/x","is_bare":false,
                 "is_detached":false,"is_prunable":false,"is_linked_worktree":true,
                 "open_workspace_id":"w1","label":"x"}
  }
}`

// bareShape is the other spelling wt tolerates: the data object alone, with no
// envelope. Which of the two herdr actually sets in HERDR_PLUGIN_EVENT_JSON is
// decided in its plugin runtime rather than its wire schema, and was not
// verified at runtime -- so both must work.
const bareShape = `{"type":"worktree_created",
  "worktree":{"path":"/r/.worktrees/y","branch":"hotfix/y"}}`

func TestParseAcceptsEnvelopeShape(t *testing.T) {
	wt, err := ParseWorktreeCreated(envelopeShape)
	if err != nil {
		t.Fatalf("envelope shape rejected: %v", err)
	}
	if wt.Path != "/r/.worktrees/x" {
		t.Errorf("path = %q, want /r/.worktrees/x", wt.Path)
	}
	if wt.Branch != "feature/x" {
		t.Errorf("branch = %q, want feature/x", wt.Branch)
	}
}

func TestParseAcceptsBareDataShape(t *testing.T) {
	wt, err := ParseWorktreeCreated(bareShape)
	if err != nil {
		t.Fatalf("bare shape rejected: %v", err)
	}
	if wt.Path != "/r/.worktrees/y" {
		t.Errorf("path = %q, want /r/.worktrees/y", wt.Path)
	}
}

// TestParseIgnoresOtherEvents matters because the hook is wired to one event
// today but plugin manifests are edited by hand. A payload for a different
// event must be distinguishable from a broken one, so the caller can exit 0
// on the first and non-zero on the second.
func TestParseIgnoresOtherEvents(t *testing.T) {
	for _, payload := range []string{
		`{"event":"tab.created","data":{"type":"tab_created"}}`,
		`{"type":"worktree_removed","worktree":{"path":"/r/x"}}`,
	} {
		_, err := ParseWorktreeCreated(payload)
		if !errors.Is(err, ErrNotWorktreeCreated) {
			t.Errorf("payload %s: err = %v, want ErrNotWorktreeCreated", payload, err)
		}
	}
}

// TestParseRejectsBrokenPayloads pins the failures that must be loud. Each of
// these means the hook is misconfigured or herdr changed its contract; a
// silent no-op there leaves worktrees unbootstrapped, which is precisely the
// failure the hook exists to prevent.
func TestParseRejectsBrokenPayloads(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"whitespace only":  "   \n ",
		"not json":         "worktree.created",
		"truncated json":   `{"event":"worktree.created","data":{`,
		"no worktree path": `{"event":"worktree.created","data":{"type":"worktree_created","worktree":{"branch":"b"}}}`,
		"empty path":       `{"event":"worktree.created","data":{"type":"worktree_created","worktree":{"path":""}}}`,
	}
	for name, payload := range cases {
		_, err := ParseWorktreeCreated(payload)
		if err == nil {
			t.Errorf("%s: parsed without error, want a failure", name)
			continue
		}
		if errors.Is(err, ErrNotWorktreeCreated) {
			t.Errorf("%s: classified as a different event; it is a malformed payload and must be loud", name)
		}
	}
}

// TestParseSurvivesUnknownFields guards forward compatibility explicitly.
// herdr is under active development and adds event fields; wt reading two of
// them must not turn a new field into a parse failure.
func TestParseSurvivesUnknownFields(t *testing.T) {
	payload := `{"event":"worktree.created","data":{"type":"worktree_created",
	  "worktree":{"path":"/r/z","branch":"b","some_future_field":{"nested":[1,2,3]}},
	  "another_future_field":true}}`
	wt, err := ParseWorktreeCreated(payload)
	if err != nil {
		t.Fatalf("unknown fields broke the parse: %v", err)
	}
	if wt.Path != "/r/z" {
		t.Errorf("path = %q, want /r/z", wt.Path)
	}
}

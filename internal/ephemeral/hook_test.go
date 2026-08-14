package ephemeral_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/ephemeral"
	"github.com/theczechr/wt/internal/gittest"
)

func TestShouldReapIsADenyList(t *testing.T) {
	// clear and resume fire while the user is still working in the
	// directory; reaping on either would delete a live session's worktree.
	for _, reason := range []string{"clear", "resume"} {
		if ephemeral.ShouldReap(reason) {
			t.Errorf("reason %q must not reap", reason)
		}
	}
	// "other" is the catch-all a normal quit most plausibly arrives as. An
	// allow-list would make the feature silently never fire.
	for _, reason := range []string{"other", "prompt_input_exit", "logout",
		"bypass_permissions_disabled", "", "something_new_in_a_later_release"} {
		if !ephemeral.ShouldReap(reason) {
			t.Errorf("reason %q must reap", reason)
		}
	}
}

func TestParseSessionEndReadsTheDocumentedPayload(t *testing.T) {
	body := `{"session_id":"abc","prompt_id":"p","transcript_path":"/t.jsonl",
	          "cwd":"/repos/server/.worktrees/x","hook_event_name":"SessionEnd",
	          "reason":"other"}`
	got, err := ephemeral.ParseSessionEnd(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.Cwd != "/repos/server/.worktrees/x" {
		t.Errorf("Cwd = %q", got.Cwd)
	}
	if got.Reason != "other" || got.SessionID != "abc" {
		t.Errorf("got %+v", got)
	}
}

func TestParseSessionEndRejectsGarbage(t *testing.T) {
	if _, err := ephemeral.ParseSessionEnd(strings.NewReader("not json")); err == nil {
		t.Error("garbage payload must be an error, not an empty struct that reaps cwd")
	}
}

// TestParseSessionEndRejectsMissingCwd covers valid JSON that nonetheless
// carries no cwd. Unlike garbage input, {} and {"reason":"other"} decode
// cleanly, leaving Cwd == "" -- and "" is not a harmlessly-absent value: `git
// -C ""` silently means "use my own working directory", so an empty Cwd
// would resolve to the wt process's own cwd and put an unrelated worktree up
// for removal, the exact failure mode ParseSessionEnd's own doc comment
// warns about.
func TestParseSessionEndRejectsMissingCwd(t *testing.T) {
	for name, body := range map[string]string{
		"empty object":         `{}`,
		"other fields, no cwd": `{"session_id":"abc","reason":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ephemeral.ParseSessionEnd(strings.NewReader(body)); err == nil {
				t.Error("a payload with no cwd must be an error, not a zero Cwd that resolves to wt's own directory")
			}
		})
	}
}

func TestWorktreeRootResolvesFromASubdirectory(t *testing.T) {
	r := gittest.New(t)
	sub := filepath.Join(r.Primary, "deep", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ephemeral.WorktreeRoot(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != filepath.Base(r.Primary) {
		t.Errorf("WorktreeRoot(%s) = %s, want %s", sub, got, r.Primary)
	}
}

func TestWorktreeRootErrorsOutsideARepo(t *testing.T) {
	if _, err := ephemeral.WorktreeRoot(context.Background(), t.TempDir()); err == nil {
		t.Error("a non-repo directory must be an error")
	}
}

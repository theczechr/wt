package discover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theczechr/ephemera/internal/model"
)

func TestClassifyT3CodeWorktree(t *testing.T) {
	t.Setenv("T3CODE_HOME", "/Users/alice/.t3")
	primary := "/Users/alice/code/server"
	cases := map[string]model.Kind{
		"/Users/alice/.t3/worktrees/server/t3code-11ab65f9": model.KindT3,
		"/Users/alice/.t3/worktreesX/server/t3code-1":       model.KindForeign,
		"/Users/alice/.sprout/worktrees/server/sprout-1":    model.KindForeign,
		"/Users/alice/code/server-cache":                    model.KindSibling,
	}
	for path, want := range cases {
		if got := classify(path, primary); got != want {
			t.Errorf("classify(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestReadSessionTailTakesNewestEntrypoint(t *testing.T) {
	// A session started in the terminal and later continued from T3 Code:
	// the newest driver is the SDK, so wt must not offer to resume it.
	body := `{"type":"user","entrypoint":"cli","cwd":"/tmp/wtA","sessionId":"s1"}
{"type":"assistant","entrypoint":"cli"}
{"type":"user","entrypoint":"sdk-ts","cwd":"/tmp/wtA","sessionId":"s1"}
{"type":"assistant","entrypoint":"sdk-ts"}
`
	p := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSessionTail(p)
	if err != nil {
		t.Fatalf("ReadSessionTail: %v", err)
	}
	if s.Entrypoint != model.EntrypointSDK {
		t.Errorf("Entrypoint = %q, want %q", s.Entrypoint, model.EntrypointSDK)
	}
	if s.ResumableHere() {
		t.Error("an SDK-hosted session must not be resumable from the terminal")
	}
}

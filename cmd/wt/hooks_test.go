package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const existingSettings = `{
  "model": "opus",
  "hooks": {
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "/x/portwatch hook end", "timeout": 10}]}
    ]
  }
}`

func TestMergeSessionEndHookAppendsAndPreservesOthers(t *testing.T) {
	out, changed, err := mergeSessionEndHook([]byte(existingSettings), "/bin/wt reap")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatal(err)
	}
	if s["model"] != "opus" {
		t.Error("unrelated keys must survive")
	}
	groups := s["hooks"].(map[string]any)["SessionEnd"].([]any)
	if len(groups) != 2 {
		t.Fatalf("want the portwatch group plus ours, got %d groups", len(groups))
	}
	if !strings.Contains(string(out), "portwatch hook end") || !strings.Contains(string(out), "/bin/wt reap") {
		t.Errorf("output lost a hook:\n%s", out)
	}
}

func TestMergeSessionEndHookIsIdempotent(t *testing.T) {
	once, _, _ := mergeSessionEndHook([]byte(existingSettings), "/bin/wt reap")
	_, changed, err := mergeSessionEndHook(once, "/other/path/wt reap")
	if err != nil || changed {
		t.Fatalf("a second install must be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestMergeSessionEndHookOnEmptyFile(t *testing.T) {
	out, changed, err := mergeSessionEndHook(nil, "/bin/wt reap")
	if err != nil || !changed || !strings.Contains(string(out), `"SessionEnd"`) {
		t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
	}
}

func TestMergeSessionEndHookRefusesMalformed(t *testing.T) {
	for _, body := range []string{`{not json`, `{"hooks": []}`, `{"hooks": {"SessionEnd": {}}}`} {
		if _, _, err := mergeSessionEndHook([]byte(body), "/bin/wt reap"); err == nil {
			t.Errorf("%s: want an error, never an overwrite", body)
		}
	}
}

func TestRunHooksInstallWithYesWritesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(existingSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runHooks([]string{"install", "claude", "--yes"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), " reap") {
		t.Errorf("settings.json not updated:\n%s", body)
	}
	if bak, err := os.ReadFile(p + ".wt-backup"); err != nil || string(bak) != existingSettings {
		t.Errorf("backup missing or wrong: %v", err)
	}
}

func TestRunHooksInstallWithoutTTYOrYesRefuses(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := runHooks([]string{"install", "claude"}, strings.NewReader(""), &out, &errb); code == 0 {
		t.Fatal("must refuse to edit settings unattended without --yes")
	}
}

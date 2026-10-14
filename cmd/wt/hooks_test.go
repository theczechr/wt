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
	ours := groups[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if ours["timeout"] != float64(30) {
		t.Errorf("our hook timeout = %v, want 30", ours["timeout"])
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

func TestMergeSessionEndHookRecognisesQuotedPathWithSpaces(t *testing.T) {
	cmd := hookCommand("/Users/a b/bin/wt")
	once, changed, err := mergeSessionEndHook([]byte(existingSettings), cmd)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(once), `'/Users/a b/bin/wt' reap`) {
		t.Errorf("command not quoted:\n%s", once)
	}
	_, changed, err = mergeSessionEndHook(once, cmd)
	if err != nil || changed {
		t.Fatalf("re-install of quoted path must be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestIsWtReap(t *testing.T) {
	cases := map[string]bool{
		"/a/wt reap":            true,
		`'/a b/wt' reap`:        true,
		`"/a b/wt" reap`:        true,
		"/a/wt reap --all":      false,
		"/a/wtx reap":           false,
		"/a b/wt reap":          false,
		"/a/wt status":          false,
		"reap":                  false,
		"/x/portwatch hook end": false,
	}
	for cmd, want := range cases {
		if got := isWtReap(cmd); got != want {
			t.Errorf("isWtReap(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/wt": "/usr/local/bin/wt",
		"/a b/wt":           "'/a b/wt'",
		"/a'b/wt":           `'/a'\''b/wt'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeSessionEndHookOnEmptyFile(t *testing.T) {
	out, changed, err := mergeSessionEndHook(nil, "/bin/wt reap")
	if err != nil || !changed || !strings.Contains(string(out), `"SessionEnd"`) {
		t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
	}
}

func TestMergeSessionEndHookOnJSONNull(t *testing.T) {
	out, changed, err := mergeSessionEndHook([]byte("null"), "/bin/wt reap")
	if err != nil || !changed || !strings.Contains(string(out), `"SessionEnd"`) {
		t.Fatalf("null body must act as empty settings: changed=%v err=%v out=%s", changed, err, out)
	}
}

func TestMergeSessionEndHookKeepsCommandTextUnescaped(t *testing.T) {
	body := `{"hooks": {"SessionEnd": [{"hooks": [{"type": "command", "command": "make a && b < c > d"}]}]}}`
	out, _, err := mergeSessionEndHook([]byte(body), "/bin/wt reap")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"make a && b < c > d"`) {
		t.Errorf("other hook's command was re-escaped:\n%s", out)
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
	if !strings.Contains(out.String(), `"timeout": 30`) {
		t.Errorf("stdout should print the hook snippet:\n%s", out.String())
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), " reap") {
		t.Errorf("settings.json not updated:\n%s", body)
	}
	if bak, err := os.ReadFile(p + ".wt-backup"); err != nil || string(bak) != existingSettings {
		t.Errorf("backup missing or wrong: %v", err)
	}
}

func TestRunHooksInstallKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	target := filepath.Join(dir, "dotfiles", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(existingSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runHooks([]string{"install", "claude", "--yes"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings.json must still be a symlink: mode=%v err=%v", fi.Mode(), err)
	}
	body, _ := os.ReadFile(target)
	if !strings.Contains(string(body), " reap") {
		t.Errorf("symlink target not updated:\n%s", body)
	}
	if bak, err := os.ReadFile(target + ".wt-backup"); err != nil || string(bak) != existingSettings {
		t.Errorf("backup next to the target missing or wrong: %v", err)
	}
}

func TestRunHooksInstallWithoutTTYOrYesRefuses(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := runHooks([]string{"install", "claude"}, strings.NewReader(""), &out, &errb); code == 0 {
		t.Fatal("must refuse to edit settings unattended without --yes")
	}
}

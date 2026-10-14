package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/theczechr/ephemera/internal/ui"
)

const hooksUsage = "usage: wt hooks install claude [--yes]"

// claudeSettingsPath is $CLAUDE_CONFIG_DIR/settings.json, else
// ~/.claude/settings.json.
func claudeSettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// isWtReap matches a hook command that already runs `<anything>/wt reap`.
func isWtReap(cmd string) bool {
	f := strings.Fields(cmd)
	return len(f) == 2 && filepath.Base(f[0]) == "wt" && f[1] == "reap"
}

// mergeSessionEndHook adds a SessionEnd command hook running cmdline to a
// Claude Code settings.json body. It returns changed=false when a `wt reap`
// hook is already present. A body whose hooks are not the documented shape
// is an error: guessing would risk dropping the user's other hooks.
//
// Keys are re-serialised in sorted order (encoding/json), so the file's
// key order may change; its content does not.
func mergeSessionEndHook(body []byte, cmdline string) ([]byte, bool, error) {
	settings := map[string]any{}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &settings); err != nil {
			return nil, false, fmt.Errorf("settings.json is not valid JSON: %w", err)
		}
	}
	hooks := map[string]any{}
	if raw, ok := settings["hooks"]; ok {
		h, ok := raw.(map[string]any)
		if !ok {
			return nil, false, errors.New(`settings.json "hooks" is not an object`)
		}
		hooks = h
	}
	var groups []any
	if raw, ok := hooks["SessionEnd"]; ok {
		g, ok := raw.([]any)
		if !ok {
			return nil, false, errors.New(`settings.json "hooks.SessionEnd" is not a list`)
		}
		groups = g
	}
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); isWtReap(c) {
				return body, false, nil
			}
		}
	}
	groups = append(groups, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": cmdline}},
	})
	hooks["SessionEnd"] = groups
	settings["hooks"] = hooks
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

// runHooks implements `wt hooks install claude [--yes]`. It never edits the
// file without consent: on a terminal it asks; without one, --yes is
// required. The previous file is kept as settings.json.wt-backup.
func runHooks(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	yes := false
	var rest []string
	for _, a := range args {
		if a == "--yes" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 2 || rest[0] != "install" || rest[1] != "claude" {
		fmt.Fprintln(stderr, hooksUsage)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	cmdline := exe + " reap"
	path := claudeSettingsPath()
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	out, changed, err := mergeSessionEndHook(body, cmdline)
	if err != nil {
		fmt.Fprintf(stderr, "wt: hooks: %s: %v\n", path, err)
		return 1
	}
	if !changed {
		fmt.Fprintf(stdout, "a `wt reap` SessionEnd hook is already in %s\n", path)
		return 0
	}
	fmt.Fprintf(stdout, "adds to %s:\n  SessionEnd → %s\n", path, cmdline)
	if !yes {
		if !ui.Interactive() {
			fmt.Fprintln(stderr, "wt: hooks: not a terminal; rerun with --yes to apply")
			return 1
		}
		fmt.Fprint(stdout, "apply? [y/N] ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "left unchanged")
			return 0
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	if body != nil {
		if err := os.WriteFile(path+".wt-backup", body, 0o600); err != nil {
			fmt.Fprintln(stderr, "wt: hooks: writing backup:", err)
			return 1
		}
	}
	tmp := path + ".wt-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	fmt.Fprintln(stdout, "installed; previous file saved as", path+".wt-backup")
	return 0
}

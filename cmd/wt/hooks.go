package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/theczechr/ephemera/internal/ui"
)

const (
	hooksUsage        = "usage: wt hooks install claude [--yes]"
	sessionEndTimeout = 30
)

var plainWordRe = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// claudeSettingsPath is $CLAUDE_CONFIG_DIR/settings.json, else
// ~/.claude/settings.json.
func claudeSettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// shellQuote makes s safe as one word in a POSIX shell command line. Paths
// made only of plain characters are returned as-is.
func shellQuote(s string) string {
	if plainWordRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hookCommand is the SessionEnd command line that runs exe's reap subcommand.
func hookCommand(exe string) string {
	return shellQuote(exe) + " reap"
}

// isWtReap matches a hook command that runs `<anything>/wt reap`, with the
// program path optionally single- or double-quoted. The command must end in
// the single word `reap`.
func isWtReap(cmd string) bool {
	prog, ok := strings.CutSuffix(strings.TrimSpace(cmd), " reap")
	if !ok {
		return false
	}
	prog = strings.TrimSpace(prog)
	if n := len(prog); n >= 2 && (prog[0] == '\'' || prog[0] == '"') && prog[n-1] == prog[0] {
		prog = prog[1 : n-1]
	} else if strings.ContainsAny(prog, " \t") {
		return false
	}
	return filepath.Base(prog) == "wt"
}

// sessionEndGroup is the settings.json hook group wt adds under SessionEnd.
func sessionEndGroup(cmdline string) map[string]any {
	return map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": cmdline,
			"timeout": sessionEndTimeout,
		}},
	}
}

// encodeJSON pretty-prints v with two-space indentation, leaving <, > and &
// unescaped so other hooks' commands keep their exact text.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
		if settings == nil {
			settings = map[string]any{}
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
	groups = append(groups, sessionEndGroup(cmdline))
	hooks["SessionEnd"] = groups
	settings["hooks"] = hooks
	out, err := encodeJSON(settings)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// resolveSettingsTarget returns the file that holds the settings: path itself,
// or what it links to, so that writes update a symlinked dotfile in place.
// A dangling symlink is an error rather than a new file.
func resolveSettingsTarget(path string) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err == nil {
		return target, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a dangling symlink", path)
	}
	return path, nil
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
	cmdline := hookCommand(exe)
	path := claudeSettingsPath()
	target, err := resolveSettingsTarget(path)
	if err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	body, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	out, changed, err := mergeSessionEndHook(body, cmdline)
	if err != nil {
		fmt.Fprintf(stderr, "wt: hooks: %s: %v\n", target, err)
		return 1
	}
	if !changed {
		fmt.Fprintf(stdout, "a `wt reap` SessionEnd hook is already in %s\n", target)
		return 0
	}
	snippet, err := encodeJSON(sessionEndGroup(cmdline))
	if err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	fmt.Fprintf(stdout, "adds to %s under hooks.SessionEnd:\n%s", target, snippet)
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
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	if body != nil {
		if err := os.WriteFile(target+".wt-backup", body, 0o600); err != nil {
			fmt.Fprintln(stderr, "wt: hooks: writing backup:", err)
			return 1
		}
	}
	tmp := target + ".wt-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		fmt.Fprintln(stderr, "wt: hooks:", err)
		return 1
	}
	fmt.Fprintln(stdout, "installed; previous file saved as", target+".wt-backup")
	return 0
}

package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHerdr installs a stub on HERDR_BIN_PATH that prints body and exits
// with code. It exists so the shell-out path is exercised for real -- argv
// construction, output scanning, error mapping -- without a herdr daemon.
func fakeHerdr(t *testing.T, body string, code int) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "herdr-stub")
	script := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(body) + "\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", stub)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i))
}

// TestInPluginPaneIsNarrow pins the discriminator. Getting this wrong in
// either direction is a silent failure: too broad and a shell-launched wt
// stops using the wrapper that works; too narrow and the overlay's enter key
// does nothing at all.
func TestInPluginPaneIsNarrow(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_ENTRYPOINT_ID", "")
	// Being inside herdr is NOT the question -- a shell in a herdr pane still
	// has the wrapper, and must keep using it.
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	if InPluginPane() {
		t.Error("a shell running inside a herdr pane was treated as a plugin pane; it still has the zsh wrapper")
	}

	t.Setenv("HERDR_PLUGIN_ENTRYPOINT_ID", "dashboard")
	if !InPluginPane() {
		t.Error("a plugin pane entrypoint was not detected; enter would silently do nothing")
	}
}

func TestBinPrefersInjectedPath(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
	if got := bin(); got != "herdr" {
		t.Errorf("fallback = %q, want herdr", got)
	}
	t.Setenv("HERDR_BIN_PATH", "/opt/herdr/bin/herdr")
	if got := bin(); got != "/opt/herdr/bin/herdr" {
		t.Errorf("bin() = %q, want the injected path", got)
	}
}

func TestOpenWorktreeReadsPaneAndAlreadyOpen(t *testing.T) {
	fakeHerdr(t, `{"id":"cli:worktree","result":{"type":"worktree_opened",`+
		`"already_open":true,"root_pane":{"pane_id":"w2:p1","cwd":"/r/wt"},`+
		`"workspace":{"workspace_id":"w2"},"worktree":{"path":"/r/wt"}}}`, 0)

	got, err := OpenWorktree(context.Background(), "/r/wt")
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	if got.PaneID != "w2:p1" {
		t.Errorf("PaneID = %q, want w2:p1", got.PaneID)
	}
	if got.WorkspaceID != "w2" {
		t.Errorf("WorkspaceID = %q, want w2", got.WorkspaceID)
	}
	if !got.AlreadyOpen {
		t.Error("AlreadyOpen = false; the caller would start a second agent under a live session")
	}
}

// TestOpenWorktreeSurfacesHerdrError checks that herdr's own structured
// error reaches the user instead of a bare non-zero exit status, which says
// nothing about what went wrong.
func TestOpenWorktreeSurfacesHerdrError(t *testing.T) {
	fakeHerdr(t, `{"id":"cli:worktree","error":{"code":"not_found","message":"no worktree at /nope"}}`, 1)

	_, err := OpenWorktree(context.Background(), "/nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"not_found", "no worktree at /nope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestDecodeScansMultipleLines guards the assumption that herdr's CLI emits
// exactly one line. It speaks newline-delimited JSON, so leading log or
// progress lines must not break the parse.
func TestDecodeScansMultipleLines(t *testing.T) {
	out := []byte("starting up\n" +
		`{"id":"x","result":{"already_open":false,"root_pane":{"pane_id":"p9"}}}` + "\n")
	res, err := decode(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(string(res), "p9") {
		t.Errorf("wrong object decoded: %s", res)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := decode([]byte("not json at all\n")); err == nil {
		t.Error("garbage parsed without error")
	}
}

func TestResumeClaudeRefusesWithoutAPane(t *testing.T) {
	if err := ResumeClaude(context.Background(), "", "sess-1", "label"); err == nil {
		t.Error("resume with no pane should fail rather than shell out")
	}
}

func TestResumeClaudeSucceedsOnOKResponse(t *testing.T) {
	fakeHerdr(t, `{"id":"cli:agent","result":{"type":"agent_started"}}`, 0)
	if err := ResumeClaude(context.Background(), "w2:p1", "sess-1", "wt"); err != nil {
		t.Errorf("ResumeClaude: %v", err)
	}
}

// writeFakeSocket creates a file where Running() looks for herdr's socket,
// so tests can simulate a daemon being up without one.
func writeFakeSocket(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "herdr.sock")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

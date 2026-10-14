package discover

import (
	"context"
	"os"
	"testing"
)

func TestIsClaudeSessionMatchesResume(t *testing.T) {
	const id = "ffe848b4-2b2a-491f-8782-53738881b77b"
	for _, cmd := range []string{
		"claude --output-format stream-json --resume=" + id + " --verbose",
		"/Users/alice/.local/bin/claude --resume " + id,
	} {
		got, ok := IsClaudeSession(cmd)
		if !ok || got != id {
			t.Errorf("IsClaudeSession(%q) = %q, %v; want %q, true", cmd, got, ok, id)
		}
	}
	// --session-id names the running session; a --resume beside it (with
	// --fork-session) names the one it was forked from.
	both := "claude --resume 22222222-2222-2222-2222-222222222222 --fork-session --session-id " + id
	if got, _ := IsClaudeSession(both); got != id {
		t.Errorf("with both flags, id = %q, want the --session-id %q", got, id)
	}
	if _, ok := IsClaudeSession("claude --resume /Users/alice/.claude/projects/x.jsonl"); ok {
		t.Error("a --resume path is not a session id")
	}
}

func TestIsClaudeProcess(t *testing.T) {
	for cmd, want := range map[string]bool{
		"claude": true,
		"/Users/alice/.local/bin/claude --output-format stream-json --verbose":     true,
		"/Users/alice/.local/share/claude/ClaudeCode.app/Contents/MacOS/claude -x": true,
		"node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js --resume x":   true,
		"claude bg-spare --bg-spare /tmp/cc-daemon-501/x/spare/y.claim.sock":       false,
		"corpclaude --session abc123":                                              false,
		"/usr/bin/vim claude":                                                      false,
		"node /Users/alice/.claude/plugins/claude-mem/mcp-server.cjs":              false,
	} {
		if got := IsClaudeProcess(cmd); got != want {
			t.Errorf("IsClaudeProcess(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// An id-less claude process must be resolved and surfaced, not dropped for
// carrying no session id.
func TestSnapshotErrRecordsIdlessClaudeProcesses(t *testing.T) {
	fakeBin(t, "ps", `#!/bin/sh
printf '  111 00:01 claude\n'
printf '  222 00:02 claude bg-spare --bg-spare /tmp/x.claim.sock\n'
`)
	fakeBin(t, "lsof", `#!/bin/sh
printf 'p1\nn/Users/alice/wt-a\n'
`)
	procs, err := SnapshotErr(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(procs.ClaudeProcs) != 1 || procs.ClaudeProcs[0].PID != 111 || procs.ClaudeProcs[0].Cwd != "/Users/alice/wt-a" {
		t.Fatalf("ClaudeProcs = %+v, want exactly pid 111 in /Users/alice/wt-a", procs.ClaudeProcs)
	}
	if len(procs.Procs) != 0 || len(procs.SessionProcs) != 0 {
		t.Errorf("an id-less claude process belongs in ClaudeProcs only: %+v", procs)
	}
}

func TestAncestorPIDsIncludesTheParent(t *testing.T) {
	got, err := AncestorPIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got[os.Getppid()] {
		t.Errorf("AncestorPIDs = %v, want it to contain the parent %d", got, os.Getppid())
	}
	if got[os.Getpid()] {
		t.Error("the process itself is not its own ancestor")
	}
}

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
		"claude bg-spare --bg-spare /tmp/cc-daemon-501/x/spare/y.claim.sock":       true,
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
// carrying no session id. A claimed --bg-spare is one; an unclaimed spare
// sits under /tmp and is dropped like any /tmp process.
func TestSnapshotErrRecordsIdlessClaudeProcesses(t *testing.T) {
	fakeBin(t, "ps", `#!/bin/sh
printf '  111 00:01 claude\n'
printf '  222 00:02 claude bg-spare --bg-spare /tmp/x.claim.sock\n'
printf '  333 00:03 claude bg-spare --bg-spare /tmp/y.claim.sock\n'
`)
	fakeBin(t, "lsof", `#!/bin/sh
pid=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-p" ]; then pid="$a"; fi
  prev="$a"
done
case "$pid" in
  111) printf 'p111\nn/Users/alice/wt-a\n' ;;
  222) printf 'p222\nn/Users/alice/wt-b\n' ;;
  *)   printf 'p333\nn/private/tmp/cc-daemon-501/x/spare\n' ;;
esac
`)
	procs, err := SnapshotErr(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(procs.ClaudeProcs) != 2 || procs.ClaudeProcs[0].PID != 111 || procs.ClaudeProcs[1].PID != 222 ||
		procs.ClaudeProcs[1].Cwd != "/Users/alice/wt-b" {
		t.Fatalf("ClaudeProcs = %+v, want pids 111 and 222 (the claimed spare), not the /tmp spare 333", procs.ClaudeProcs)
	}
	if len(procs.Procs) != 0 || len(procs.SessionProcs) != 0 {
		t.Errorf("an id-less claude process belongs in ClaudeProcs only: %+v", procs)
	}
}

func TestAncestorChainStartsAtTheParent(t *testing.T) {
	got, err := AncestorChain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].PID != os.Getppid() || got[0].Command == "" {
		t.Fatalf("AncestorChain = %+v, want it to start at the parent %d with its command", got, os.Getppid())
	}
	for _, p := range got {
		if p.PID == os.Getpid() {
			t.Error("the process itself is not its own ancestor")
		}
	}
}

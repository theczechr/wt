package discover

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCwdOfReturnsEmptyOnCancelledContext asserts CwdOf actually threads its
// context into the lsof invocation rather than ignoring it: an
// already-cancelled context must make the underlying exec.CommandContext
// fail immediately (never actually spawning lsof to block), and CwdOf must
// degrade to "" rather than panicking or hanging.
func TestCwdOfReturnsEmptyOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := CwdOf(ctx, 1); got != "" {
		t.Errorf("CwdOf with a cancelled context = %q, want \"\"", got)
	}
}

// TestCwdOfErrReturnsErrorOnCancelledContext is CwdOfErr's counterpart to
// TestCwdOfReturnsEmptyOnCancelledContext: the distinction CwdOfErr exists
// to add is exactly a non-nil error here, where CwdOf collapses it to "".
func TestCwdOfErrReturnsErrorOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cwd, err := CwdOfErr(ctx, 1)
	if cwd != "" {
		t.Errorf("CwdOfErr with a cancelled context: cwd = %q, want \"\"", cwd)
	}
	if err == nil {
		t.Error("CwdOfErr with a cancelled context: err = nil, want non-nil -- this is the probe-failed case, not resolved-to-nothing")
	}
}

// fakeBin writes an executable shell script named name into a fresh temp
// dir and prepends that dir to PATH for the duration of the test, so exec
// calls resolve it instead of the real binary.
func fakeBin(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCwdOfErrDistinguishesResolvedFromProbeFailure fakes lsof to return a
// real cwd for one pid and fail outright for another, and asserts CwdOfErr
// reports the first as (cwd, nil) and the second as ("", non-nil) -- never
// the same value for both.
func TestCwdOfErrDistinguishesResolvedFromProbeFailure(t *testing.T) {
	fakeBin(t, "lsof", `#!/bin/sh
pid=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-p" ]; then pid="$a"; fi
  prev="$a"
done
if [ "$pid" = "111" ]; then
  printf 'p111\nn/Users/alice/worktree-a\n'
  exit 0
fi
echo "fake lsof: permission denied" >&2
exit 1
`)

	cwd, err := CwdOfErr(context.Background(), 111)
	if err != nil {
		t.Fatalf("resolved pid: err = %v, want nil", err)
	}
	if cwd != "/Users/alice/worktree-a" {
		t.Errorf("resolved pid: cwd = %q, want /Users/alice/worktree-a", cwd)
	}

	cwd, err = CwdOfErr(context.Background(), 222)
	if err == nil {
		t.Fatal("failed probe: err = nil, want non-nil")
	}
	if cwd != "" {
		t.Errorf("failed probe: cwd = %q, want \"\"", cwd)
	}
}

// TestSnapshotErrCountsUnresolvedCwdsSeparatelyFromResolved is the
// SnapshotErr-level version of the CwdOfErr test above: with one candidate
// process resolving cleanly and another's lsof probe failing outright, the
// resolved one must land in Procs and the failed one must be counted in
// UnresolvedCwds, not silently dropped as if it simply weren't in any
// worktree.
func TestSnapshotErrCountsUnresolvedCwdsSeparatelyFromResolved(t *testing.T) {
	fakeBin(t, "ps", `#!/bin/sh
printf '  111 00:01 deno run -A resolved.ts\n'
printf '  222 00:02 deno run -A unresolved.ts\n'
`)
	fakeBin(t, "lsof", `#!/bin/sh
pid=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-p" ]; then pid="$a"; fi
  prev="$a"
done
if [ "$pid" = "111" ]; then
  printf 'p111\nn/Users/alice/worktree-a\n'
  exit 0
fi
echo "fake lsof: permission denied" >&2
exit 1
`)

	procs, err := SnapshotErr(context.Background())
	if err != nil {
		t.Fatalf("SnapshotErr returned an error: %v", err)
	}
	if procs.UnresolvedCwds != 1 {
		t.Errorf("UnresolvedCwds = %d, want 1", procs.UnresolvedCwds)
	}
	if len(procs.Procs) != 1 || procs.Procs[0].PID != 111 {
		t.Fatalf("Procs = %+v, want exactly pid 111", procs.Procs)
	}
	if procs.Procs[0].Cwd != "/Users/alice/worktree-a" {
		t.Errorf("resolved proc cwd = %q, want /Users/alice/worktree-a", procs.Procs[0].Cwd)
	}
	for _, p := range procs.Procs {
		if strings.Contains(p.Command, "unresolved") {
			t.Errorf("the unresolved-cwd process must not appear in Procs, got %+v", p)
		}
	}
}

func TestIsClaudeSessionMatchesRealCommandLines(t *testing.T) {
	live := "/Users/alice/.local/share/claude/versions/2.1.220 --session-id ffe848b4-2b2a-491f-8782-53738881b77b --fork-session --resume /Users/alice/.claude/projects/x.jsonl"
	id, ok := IsClaudeSession(live)
	if !ok {
		t.Fatal("real session command must match")
	}
	if id != "ffe848b4-2b2a-491f-8782-53738881b77b" {
		t.Errorf("id = %q", id)
	}

	// Pre-warmed spares must NOT count as sessions.
	spare := "claude bg-pty-host --bg-pty-host /tmp/cc-daemon-501/3ff8a2c8/spare/3c3db37f.pty.sock 200 50 -- /Users/alice/.local/share/claude/versions/2.1.229 --bg-spare /tmp/cc-daemon-501/3ff8a2c8/spare/3c3db37f.claim.sock"
	if _, ok := IsClaudeSession(spare); ok {
		t.Error("--bg-spare process must not count as a live session")
	}

	if _, ok := IsClaudeSession("deno run -A foo.ts"); ok {
		t.Error("unrelated process must not match")
	}
}

// TestIsClaudeSessionMatchesEqualsForm asserts the "--session-id=<uuid>"
// form is recognised, not just the space-separated "--session-id <uuid>"
// form. A command line using "=" would otherwise silently fail to match,
// marking a live session idle.
func TestIsClaudeSessionMatchesEqualsForm(t *testing.T) {
	live := "/Users/alice/.local/share/claude/versions/2.1.220 --session-id=ffe848b4-2b2a-491f-8782-53738881b77b --fork-session"
	id, ok := IsClaudeSession(live)
	if !ok {
		t.Fatal("the --session-id=<uuid> form must match")
	}
	if id != "ffe848b4-2b2a-491f-8782-53738881b77b" {
		t.Errorf("id = %q", id)
	}
}

func TestInterestingExcludesAgentInfrastructureButCatchesRealWork(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{
			name: "claude-mem MCP server inline node script",
			cmd:  `node -e const f=require('fs'),p=require('path');const dir=p.join(require('os').homedir(),'.claude-mem');`,
			want: false,
		},
		{
			name: "corpclaude",
			cmd:  "corpclaude --session abc123",
			want: false,
		},
		{
			// Round 2: a name-based allowlist ("node") plus a literal
			// denylist ("mcp-server") let this MCP server through, since
			// its binary is named context7-mcp, not "mcp-server".
			name: "context7 MCP server run via npx cache",
			cmd:  "node /Users/alice/.npm/_npx/eea2bd7412d4593b/node_modules/.bin/context7-mcp",
			want: false,
		},
		{
			name: "chroma-mcp launched via uv tool uvx",
			cmd:  "/opt/homebrew/bin/uv tool uvx --python 3.13 --with onnxruntime>=1.20 --from chroma-mcp==0.2.6 chroma-mcp",
			want: false,
		},
		{
			name: "chroma-mcp launched via a uv-cached python interpreter",
			cmd:  "/Users/alice/.cache/uv/archive-v0/ANnpuwKfjbvElpRp/bin/python /Users/alice/.cache/uv/archive-v0/ANnpuwKfjbvElpRp/bin/chroma-mcp",
			want: false,
		},
		{
			name: "claude-mem MCP server inline node script (round 2 fixture)",
			cmd:  "node -e const f=require('fs'),p=require('path'),o=require('os')",
			want: false,
		},
		{
			name: "corpclaude launched via nvm-managed node",
			cmd:  "node /Users/alice/.nvm/versions/node/v25.6.1/bin/corpclaude",
			want: false,
		},
		{
			name: "real long-running work: deno rerun-sync",
			cmd:  "deno run -A --v8-flags=--max-old-space-size=8192 -c /Users/alice/code/server-invites/deno.json --env-file=.env.prod /tmp/.../rerun-sync.ts",
			want: true,
		},
	}
	for _, c := range cases {
		if got := interesting(c.cmd); got != c.want {
			t.Errorf("%s: interesting(%q) = %v, want %v", c.name, c.cmd, got, c.want)
		}
	}
}

func TestParsePSExtractsPidAndCommand(t *testing.T) {
	out := "  93927     03:40 deno run -A --env-file=.env.prod rerun-sync.ts\n" +
		"  49877  10:53:42 /Users/alice/.local/bin/claude daemon run\n"
	got := ParsePS(out)
	if len(got) != 2 {
		t.Fatalf("got %d procs, want 2", len(got))
	}
	if got[0].PID != 93927 {
		t.Errorf("PID = %d, want 93927", got[0].PID)
	}
	if got[0].Elapsed != "03:40" {
		t.Errorf("Elapsed = %q, want 03:40", got[0].Elapsed)
	}
	if got[0].Command != "deno run -A --env-file=.env.prod rerun-sync.ts" {
		t.Errorf("Command = %q", got[0].Command)
	}
}

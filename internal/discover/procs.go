package discover

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/theczechr/wt/internal/model"
)

// [=\s]+ accepts both the space-separated form ("--session-id <uuid>") and
// the "--session-id=<uuid>" form; matching only \s would silently miss the
// latter and mark a live session idle.
var sessionIDRe = regexp.MustCompile(`--session-id[=\s]+([0-9a-f-]{36})`)

// IsClaudeSession reports whether a command line belongs to a real Claude
// session, and extracts its session id. Pre-warmed --bg-spare processes are
// excluded: their cwd is under /tmp and they belong to no worktree.
func IsClaudeSession(cmd string) (string, bool) {
	if strings.Contains(cmd, "--bg-spare") {
		return "", false
	}
	m := sessionIDRe.FindStringSubmatch(cmd)
	if m == nil {
		return "", false
	}
	return m[1], true
}

var psLineRe = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+(.*)$`)

// ParsePS parses `ps -eo pid=,etime=,command=` output.
func ParsePS(out string) []model.Proc {
	var res []model.Proc
	for _, line := range strings.Split(out, "\n") {
		m := psLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		res = append(res, model.Proc{PID: pid, Elapsed: m[2], Command: m[3]})
	}
	return res
}

// CwdOf resolves a process's working directory, collapsing a probe failure
// to "". Kept, unchanged, for the reap path (via Snapshot/SnapshotErr) and
// any caller that has never needed to tell "not in any worktree" apart
// from "couldn't check" -- Reap's predicate already ignores an unresolved
// cwd either way (see ephemeral.inspect), so the distinction would be
// discarded immediately. A caller that DOES need the distinction --
// anything surfacing the result to a human before an irreversible action --
// must use CwdOfErr instead.
func CwdOf(ctx context.Context, pid int) string {
	cwd, _ := CwdOfErr(ctx, pid)
	return cwd
}

// CwdOfErr resolves a process's working directory, keeping "the probe
// itself failed" distinguishable from "the probe succeeded and found
// nothing to report."
//
// Only a failure to run or complete the probe -- lsof missing, permission
// denied, the process gone by the time lsof inspected it, ctx
// cancelled/timed out -- returns a non-nil error. lsof's own exit code
// does not distinguish "no such process" from "permission denied" from
// "lsof itself is broken" in a way this package can rely on portably, so
// all of them are folded into one "unresolved" outcome here; the caller
// (SnapshotErr) counts them, but never tries to say which cause it was.
// This is the conservative direction for a probe fed into a delete
// confirmation: it can over-count "couldn't check" (e.g. a process that
// simply exited between ps and this call) but must never under-count it,
// since under-counting is exactly the failed-probe-means-safe trap this
// package exists to avoid.
//
// A resolved probe with no "n/" line in its output (lsof ran, but the
// process had no reportable cwd entry) returns ("", nil) -- a genuine,
// benign "nothing here," not a failure.
//
// ctx bounds the call so a stale mount or an unresponsive process cannot
// hang lsof indefinitely with nothing to cancel it.
func CwdOfErr(ctx context.Context, pid int) (string, error) {
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof -p %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}
	return "", nil
}

// cwdWorkers bounds how many concurrent lsof probes Snapshot runs at once.
// lsof is the slow part of Snapshot (~158 sequential calls measured at
// ~6.9s), and each call is an independent, read-only probe of a distinct
// pid, so running them concurrently is safe.
const cwdWorkers = 16

// SessionProc is a running Claude session process together with the session
// id its command line carries.
//
// The cwd is the point of this type. Snapshot resolves it for every session
// process and then throws it away, keeping only the id -> pid map; a caller
// that wants to know whether a session is running *in a particular
// directory* is left to infer it from ~/.claude/projects, which keys on
// whichever cwd the user happened to launch from and does not exist at all
// until the first message is flushed. The process table has neither blind
// spot, so it is surfaced.
type SessionProc struct {
	model.Proc
	SessionID string
}

// Processes is one enumeration of the process table.
//
// A struct rather than a wider tuple return: Snapshot's two-value signature
// is depended on by the dashboard and must not change, so the extra field
// had to land somewhere that only SnapshotErr's callers see, and a struct
// lets later fields be added the same way without touching them again.
type Processes struct {
	// Procs is non-Claude work with a resolved cwd, sorted by pid.
	Procs []model.Proc
	// Live maps a Claude session id to the pid running it.
	Live map[string]int
	// SessionProcs is the same Claude sessions as Live, with the cwd each one
	// is actually running in.
	SessionProcs []SessionProc
	// UnresolvedCwds counts candidate processes whose cwd probe (CwdOfErr)
	// failed outright -- lsof missing, permission denied, the process gone
	// mid-probe, ctx cancelled/timed out. These processes are silently
	// absent from Procs/Live/SessionProcs, exactly like a process that was
	// genuinely resolved to a cwd outside every worktree -- from those
	// three fields alone, "definitely not in any worktree" and "could not
	// be checked" look identical. UnresolvedCwds is what lets a caller that
	// needs to warn a human (the dd delete confirmation) tell them apart,
	// without changing what Procs/Live/SessionProcs themselves contain --
	// see delete_confirm.go's unresolvedProcsWarning.
	UnresolvedCwds int
}

// Snapshot returns all processes with a resolved cwd, plus a map from Claude
// session id to the pid running it. Processes whose cwd cannot be resolved,
// or which live under /tmp, are dropped.
//
// A failed ps is swallowed here (empty results, no error) to keep every
// existing caller's signature and behaviour unchanged. None of them treat
// "nothing is running" and "couldn't check" as different outcomes. A caller
// that does need that distinction -- ephemeral.Reap, deciding whether it is
// safe to DELETE something -- must use SnapshotErr instead: silently
// returning "nothing running" for a failed probe is the exact FillStatus
// trap this package exists elsewhere to avoid, reproduced for processes.
func Snapshot(ctx context.Context) ([]model.Proc, map[string]int) {
	p, _ := SnapshotErr(ctx)
	return p.Procs, p.Live
}

// SnapshotErr is Snapshot with the ps failure reported rather than
// swallowed, and with the session processes' resolved cwds kept rather than
// discarded. Callers deciding whether to delete something must be able to
// tell an empty process list from a probe that never ran; Snapshot's
// nil-on-error return cannot express that difference.
func SnapshotErr(ctx context.Context) (Processes, error) {
	res := Processes{Live: map[string]int{}}
	out, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,etime=,command=").Output()
	if err != nil {
		return res, fmt.Errorf("ps: %w", err)
	}

	type candidate struct {
		proc      model.Proc
		id        string
		isSession bool
	}
	var candidates []candidate
	for _, p := range ParsePS(string(out)) {
		id, isSession := IsClaudeSession(p.Command)
		// Only resolve cwd for plausible candidates; lsof is the slow part.
		if !isSession && !interesting(p.Command) {
			continue
		}
		candidates = append(candidates, candidate{proc: p, id: id, isSession: isSession})
	}

	// Each goroutine writes only to its own index, so no shared state needs
	// guarding beyond the WaitGroup/semaphore that bound concurrency.
	cwds := make([]string, len(candidates))
	cwdErrs := make([]error, len(candidates))
	sem := make(chan struct{}, cwdWorkers)
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			cwds[i], cwdErrs[i] = CwdOfErr(ctx, candidates[i].proc.PID)
		}(i)
	}
	wg.Wait()

	for i, c := range candidates {
		if cwdErrs[i] != nil {
			// Unresolved, not "not in a worktree": counted separately so a
			// caller warning a human can tell the two apart. See
			// UnresolvedCwds and CwdOfErr.
			res.UnresolvedCwds++
			continue
		}
		cwd := cwds[i]
		if cwd == "" || strings.HasPrefix(cwd, "/private/tmp") || strings.HasPrefix(cwd, "/tmp") {
			continue
		}
		p := c.proc
		p.Cwd = cwd
		if c.isSession {
			res.Live[c.id] = p.PID
			// Recorded in SessionProcs but not Procs: sessions surface in the
			// sessions pane, not the processes pane. The two lists are filled
			// from the same pass so a session can never appear in one and be
			// missing from the other.
			res.SessionProcs = append(res.SessionProcs, SessionProc{Proc: p, SessionID: c.id})
			continue
		}
		res.Procs = append(res.Procs, p)
	}
	// Concurrency reorders completion, but the TUI must not reshuffle rows
	// between refreshes: sort deterministically by PID before returning.
	sort.Slice(res.Procs, func(i, j int) bool { return res.Procs[i].PID < res.Procs[j].PID })
	sort.Slice(res.SessionProcs, func(i, j int) bool { return res.SessionProcs[i].PID < res.SessionProcs[j].PID })
	return res, nil
}

// noisy matches agent, editor, and MCP infrastructure that would otherwise
// pass the inclusion keywords below. A name-based denylist alone keeps
// losing this race: node, python, and uv are the runtimes for BOTH real
// work and every MCP server, so as new MCP servers get added (context7-mcp,
// chroma-mcp, ...) each one slips through until named explicitly. Instead,
// exclude by where the executable lives — package-manager caches and plugin
// dirs are infrastructure, never the user's project — plus a
// case-insensitive "mcp" substring that catches any MCP server by name,
// present or future. Checked before inclusion, so it wins whenever both
// match.
func noisy(cmd string) bool {
	for _, k := range []string{
		"node -e",
		"corpclaude",
		"bg-pty-host",
		"--bg-spare",
		"claude daemon",
		".npm/_npx/",
		".cache/uv/",
		".claude/plugins/",
		".claude-mem",
		"uv tool uvx",
		"uvx --",
	} {
		if strings.Contains(cmd, k) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(cmd), "mcp")
}

// interesting filters ps down to long-running work worth showing.
func interesting(cmd string) bool {
	if noisy(cmd) {
		return false
	}
	for _, k := range []string{"deno", "node", "supabase", "psql", "docker", "go run", "vite", "next"} {
		if strings.Contains(cmd, k) {
			return true
		}
	}
	return false
}

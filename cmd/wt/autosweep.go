package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
)

// sweepEnv is env without the variables that point git at a specific
// repository. A wt started from a git hook or alias inherits them, and they
// override the `git -C <worktree>` every reap check runs.
func sweepEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// spawnsSweep reports whether running cmd should kick a background sweep.
// reap and hook ARE reaping; the dashboard ("") sweeps on its own refresh;
// help and timer are not worktree work.
func spawnsSweep(cmd string) bool {
	switch cmd {
	case "reap", "hook", "", "help", "-h", "--help", "timer":
		return false
	}
	return true
}

// maybeSpawnSweep starts a detached `wt reap --all --quiet` when one is due.
// Setsid detaches it from the terminal, so the foreground command never
// waits and Ctrl-C does not kill it. The child applies the sweep grace, so
// the worktree this very command may be creating is safe.
func maybeSpawnSweep(cfg config.Config, cmd string) {
	if !spawnsSweep(cmd) || !ephemeral.TriggerDue(time.Now(), cfg.EffectiveReapTriggerInterval()) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.Command(exe, "reap", "--all", "--quiet")
	c.Env = sweepEnv(os.Environ())
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() == nil {
		_ = c.Process.Release()
	}
}

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/ephemeral"
)

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
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() == nil {
		_ = c.Process.Release()
	}
}

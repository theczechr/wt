package ephemeral

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// sweepLockName is the lock file every sweep takes (spec §3.4).
const sweepLockName = "sweep.lock"

// ErrSweepBusy means another sweep holds the lock. Not a failure: the
// other sweep is doing the same work.
var ErrSweepBusy = errors.New("another sweep is already running")

// StateDir is where wt keeps reap.log, the sweep lock and the trigger
// timestamp: $WT_STATE_DIR, else ~/.local/state/wt.
func StateDir() string {
	if v := os.Getenv("WT_STATE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "wt-state")
	}
	return filepath.Join(home, ".local", "state", "wt")
}

// acquireLock takes an exclusive, non-blocking flock on StateDir()/name.
// flock locks belong to the open file description, so a second open in the
// same process conflicts exactly like a second process would. ok is false
// when someone else holds it.
func acquireLock(name string) (release func(), ok bool, err error) {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true, nil
}

// LogReap appends one line to reap.log: time, path, outcome ("reaped" or
// the error), and the reason (spec §3.4). The first three columns keep
// their old meaning, so existing greps still work.
//
// A SessionEnd hook's stderr reaches only the session transcript and its
// exit code is ignored, so without this a refusal -- the interesting case --
// would leave no trace anywhere the user looks.
func LogReap(worktree string, err error, reason string) {
	dir := StateDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "reap.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer f.Close()
	outcome := "reaped"
	if err != nil {
		outcome = err.Error()
	}
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339), worktree,
		logField.Replace(outcome), logField.Replace(reason))
}

// logField keeps a free-text column on one line and in its own column: git
// diagnostics folded into errors carry newlines, and a tab would shift every
// column after it.
var logField = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

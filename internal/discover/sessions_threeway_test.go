package discover

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/theczechr/wt/internal/model"
)

// TestSessionsForMissingDirectoryIsNotAnError pins the benign half. A
// worktree nobody ever opened Claude in has no project directory, and that
// is the common case -- reporting it as unknown would refuse to delete
// exactly the worktrees that are safest to delete.
func TestSessionsForMissingDirectoryIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // ClaudeProjectsDir resolves under HOME

	sessions, err := SessionsFor("/some/worktree/never/opened", nil)
	if err != nil {
		t.Fatalf("a never-used worktree reported an error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

// TestSessionsForUnreadableDirectoryIsAnError is the half that was missing,
// and the last of the four fail-open collectors this package documents. A
// directory that exists but cannot be read says nothing about what ran
// there, so it must not return the empty slice that means "nothing did".
func TestSessionsForUnreadableDirectoryIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not work this way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not restrict reads")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	worktree := "/r/server-locked"
	dir := filepath.Join(model.ClaudeProjectsDir(), model.ProjectDirName(worktree))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The directory exists -- so this is emphatically not the IsNotExist
	// case -- but cannot be listed.
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	sessions, err := SessionsFor(worktree, nil)
	if err == nil {
		t.Fatal("an unreadable session directory reported success; a delete gate would read it as 'no sessions'")
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions alongside the error, want 0", len(sessions))
	}
}

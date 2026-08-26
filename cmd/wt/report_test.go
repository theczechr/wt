package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/wt/internal/bootstrap"
)

// capture runs f with stderr redirected and returns what it wrote.
func capture(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	f()
	w.Close()
	os.Stderr = old
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

// TestReportStaysQuietWhenTheNameMatches: the changed shell prompt is
// already the feedback, so a matching directory name needs no line.
func TestReportStaysQuietWhenTheNameMatches(t *testing.T) {
	branch := "fix/ab-book-nan-guard"
	path := filepath.Join(t.TempDir(), bootstrap.Slug(branch))
	if out := capture(t, func() { reportDestination(branch, path) }); out != "" {
		t.Errorf("printed %q for a directory already named after the branch", out)
	}
}

// TestReportExplainsAMismatchedDirectory is the case that looked like a
// bug: git permits one worktree per branch, so a directory named for some
// other branch is still the only possible destination.
func TestReportExplainsAMismatchedDirectory(t *testing.T) {
	out := capture(t, func() {
		reportDestination("feature/telemetry-ingestion", "/r/.worktrees/fix-paypal-dispute-dedupe")
	})
	if !strings.Contains(out, "fix-paypal-dispute-dedupe") {
		t.Errorf("output does not name the destination: %q", out)
	}
	if !strings.Contains(out, "feature/telemetry-ingestion") {
		t.Errorf("output does not name the branch: %q", out)
	}
	if !strings.Contains(out, "only one worktree") {
		t.Errorf("output does not explain why: %q", out)
	}
}

// TestReportSaysWhenAlreadyThere: running wt for the branch you are already
// in moves nowhere and printed nothing, which read as a failed no-op.
func TestReportSaysWhenAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	out := capture(t, func() { reportDestination("feature/x", dir) })
	if !strings.Contains(out, "already here") {
		t.Errorf("did not report standing still: %q", out)
	}
}

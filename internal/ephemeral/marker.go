// Package ephemeral owns the lifecycle of the short-lived worktrees
// `wt <branch>` creates: the marker identifying one, the predicate deciding
// whether it may be removed, and the two paths that act on that decision (a
// SessionEnd hook and a sweep).
//
// wt's standing rule is that it deletes nothing. These worktrees are the one
// narrow exception, and every gate protecting it lives here.
package ephemeral

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MarkerName is the file identifying a worktree as wt-created and therefore
// eligible for automatic removal. Deleting it by hand promotes the worktree
// to permanent.
const MarkerName = ".wt-ephemeral"

// markerVersion is bumped when the marker's meaning changes. An unrecognised
// version is refused rather than guessed at: the file is an input to a
// destructive decision.
const markerVersion = 1

// Marker records which branch of which repo an ephemeral worktree holds.
type Marker struct {
	Version   int       `json:"version"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Primary   string    `json:"primary"`
	CreatedAt time.Time `json:"created_at"`
}

// WriteMarker records m in the worktree root.
func WriteMarker(worktree string, m Marker) error {
	m.Version = markerVersion
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(worktree, MarkerName), append(body, '\n'), 0o600)
}

// ReadMarker loads the marker from a worktree root. Every failure mode --
// absent, unreadable, malformed, unknown version, missing field -- is an
// error, because each leaves the caller unable to prove the worktree is
// wt's to delete.
func ReadMarker(worktree string) (Marker, error) {
	var m Marker
	body, err := os.ReadFile(filepath.Join(worktree, MarkerName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("%s: %w", MarkerName, err)
	}
	if m.Version != markerVersion {
		return m, fmt.Errorf("%s: unsupported version %d", MarkerName, m.Version)
	}
	if m.Repo == "" || m.Branch == "" || m.Primary == "" {
		return m, fmt.Errorf("%s: incomplete marker", MarkerName)
	}
	return m, nil
}

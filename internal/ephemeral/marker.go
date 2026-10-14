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
const markerVersion = 2

// Marker records which branch of which repo an ephemeral worktree holds.
//
// Version 2 adds the fields the done rule needs (spec §2.4). A version 1
// marker stays valid: Start is then empty, which selects the fallback
// own-commit rule in OwnCommits.
type Marker struct {
	Version   int       `json:"version"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Primary   string    `json:"primary"`
	CreatedAt time.Time `json:"created_at"`

	// Start is the commit HEAD pointed at when wt created the worktree.
	Start string `json:"start,omitempty"`
	// PR is the pull request this worktree was opened for, if any.
	PR int `json:"pr,omitempty"`
	// BranchCreatedByWT is true when wt created the local branch (a fork
	// PR's pr/<N>, or a local copy of a remote-only branch).
	BranchCreatedByWT bool `json:"branch_created_by_wt,omitempty"`
	// LastOpenedAt is the last time wt handed this worktree to the user.
	LastOpenedAt time.Time `json:"last_opened_at,omitzero"`
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
	if m.Version != 1 && m.Version != markerVersion {
		return m, fmt.Errorf("%s: unsupported version %d", MarkerName, m.Version)
	}
	if m.Repo == "" || m.Branch == "" || m.Primary == "" {
		return m, fmt.Errorf("%s: incomplete marker", MarkerName)
	}
	return m, nil
}

// UpdateMarker reads the marker, applies edit, and writes it back as the
// current version. It never creates a marker: an unreadable one is an error,
// because a marker is an input to a destructive decision.
func UpdateMarker(worktree string, edit func(*Marker)) error {
	m, err := ReadMarker(worktree)
	if err != nil {
		return err
	}
	edit(&m)
	return WriteMarker(worktree, m)
}

// TouchOpened records that wt just handed this worktree to the user. It
// feeds LastUsed and the sweep grace period.
func TouchOpened(worktree string, now time.Time) error {
	return UpdateMarker(worktree, func(m *Marker) { m.LastOpenedAt = now })
}

package model

import (
	"strings"
	"testing"
)

func TestPruneBlockersRefusesT3Worktree(t *testing.T) {
	w := Workspace{Kind: KindT3, StatusKnown: true, SessionsKnown: true}
	b := strings.Join(w.PruneBlockers(), "; ")
	if !strings.Contains(b, "T3 Code") {
		t.Fatalf("PruneBlockers() = %q, want a T3 Code blocker", b)
	}
}

func TestResumableHere(t *testing.T) {
	for ep, want := range map[string]bool{"": true, "cli": true, EntrypointSDK: false} {
		if got := (Session{Entrypoint: ep}).ResumableHere(); got != want {
			t.Errorf("ResumableHere(%q) = %v, want %v", ep, got, want)
		}
	}
}

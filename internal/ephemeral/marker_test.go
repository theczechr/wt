package ephemeral_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/ephemeral"
)

func TestMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := ephemeral.Marker{
		Version:   1,
		Repo:      "backend",
		Branch:    "feat/x",
		Primary:   "/repos/server",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := ephemeral.WriteMarker(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != want.Repo || got.Branch != want.Branch || got.Primary != want.Primary {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
}

func TestReadMarkerRefusesUnusableMarkers(t *testing.T) {
	cases := map[string]string{
		"corrupt":       "{not json",
		"wrong version": `{"version":99,"repo":"backend","branch":"b","primary":"/p"}`,
		"no repo":       `{"version":1,"branch":"b","primary":"/p"}`,
		"no branch":     `{"version":1,"repo":"backend","primary":"/p"}`,
		"no primary":    `{"version":1,"repo":"backend","branch":"b"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ephemeral.MarkerName), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ephemeral.ReadMarker(dir); err == nil {
				t.Error("must refuse an unusable marker; anything else could authorise a deletion")
			}
		})
	}
}

func TestReadMarkerAbsentIsAnError(t *testing.T) {
	if _, err := ephemeral.ReadMarker(t.TempDir()); err == nil {
		t.Error("a missing marker must be an error")
	}
}

func TestReadMarkerUnreadableIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions, so this gate cannot be exercised")
	}
	dir := t.TempDir()
	markerPath := filepath.Join(dir, ephemeral.MarkerName)
	if err := os.WriteFile(markerPath, []byte(`{"version":1,"repo":"backend","branch":"b","primary":"/p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(markerPath, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := ephemeral.ReadMarker(dir); err == nil {
		t.Error("an unreadable marker must be an error")
	}
}

func TestWriteMarkerStampsVersion(t *testing.T) {
	dir := t.TempDir()
	m := ephemeral.Marker{
		Version:   99, // intentionally wrong
		Repo:      "backend",
		Branch:    "feat/x",
		Primary:   "/repos/server",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := ephemeral.WriteMarker(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
}

func TestReadMarkerAcceptsVersionOne(t *testing.T) {
	dir := t.TempDir()
	body := `{"version":1,"repo":"server","branch":"feat/x","primary":"/p","created_at":"2026-08-13T10:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, ephemeral.MarkerName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatalf("a v1 marker must stay readable: %v", err)
	}
	if m.Start != "" {
		t.Errorf("Start = %q, want empty for v1 (selects the fallback own-commit rule)", m.Start)
	}
}

func TestMarkerV2RoundTripsNewFields(t *testing.T) {
	dir := t.TempDir()
	opened := time.Now().UTC().Truncate(time.Second)
	in := ephemeral.Marker{
		Repo: "server", Branch: "pr/7", Primary: "/p", CreatedAt: opened,
		Start: "abc123", PR: 7, BranchCreatedByWT: true, LastOpenedAt: opened,
	}
	if err := ephemeral.WriteMarker(dir, in); err != nil {
		t.Fatal(err)
	}
	got, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Start != "abc123" || got.PR != 7 || !got.BranchCreatedByWT || !got.LastOpenedAt.Equal(opened) {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

func TestTouchOpenedUpgradesV1KeepingStartEmpty(t *testing.T) {
	dir := t.TempDir()
	body := `{"version":1,"repo":"server","branch":"feat/x","primary":"/p","created_at":"2026-08-13T10:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, ephemeral.MarkerName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := ephemeral.TouchOpened(dir, now); err != nil {
		t.Fatal(err)
	}
	got, err := ephemeral.ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || !got.LastOpenedAt.Equal(now) || got.Start != "" {
		t.Errorf("got %+v, want v2 with LastOpenedAt set and Start still empty", got)
	}
}

func TestTouchOpenedOnUnmarkedDirIsAnError(t *testing.T) {
	if err := ephemeral.TouchOpened(t.TempDir(), time.Now()); err == nil {
		t.Error("touching a directory without a marker must fail, never create one")
	}
}

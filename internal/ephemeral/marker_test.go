package ephemeral_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theczechr/wt/internal/ephemeral"
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
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
}

package config

import "testing"

// TestEffectiveHerdrStartupDashboard pins the default and the tolerance for
// a bad value. A typo here must not fail the whole config load: the same
// file governs worktree deletion, and refusing to load it over a cosmetic
// key would be far worse than ignoring the key.
func TestEffectiveHerdrStartupDashboard(t *testing.T) {
	cases := map[string]string{
		"":         StartupAuto,
		"auto":     StartupAuto,
		"always":   StartupAlways,
		"never":    StartupNever,
		"Always":   StartupAuto, // case-sensitive by design; unknown falls back
		"whatever": StartupAuto,
	}
	for in, want := range cases {
		if got := (Config{HerdrStartupDashboard: in}).EffectiveHerdrStartupDashboard(); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

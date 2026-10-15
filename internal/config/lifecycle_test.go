package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theczechr/ephemera/internal/config"
)

func load(t *testing.T, body string) config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEffectiveIdleDaysDefaultsToThree(t *testing.T) {
	cfg := load(t, "[repo.server]\n")
	if got := cfg.EffectiveIdleDays("server"); got != 3 {
		t.Fatalf("EffectiveIdleDays = %d, want 3", got)
	}
}

func TestEffectiveIdleDaysPrefersRepoOverGlobal(t *testing.T) {
	cfg := load(t, "idle_days = 7\n[repo.server]\nidle_days = 2\n[repo.web]\n")
	if got := cfg.EffectiveIdleDays("server"); got != 2 {
		t.Errorf("server = %d, want the repo's own 2", got)
	}
	if got := cfg.EffectiveIdleDays("web"); got != 7 {
		t.Errorf("web = %d, want the global 7", got)
	}
}

func TestEffectiveIdleDaysIgnoresNonPositive(t *testing.T) {
	cfg := load(t, "idle_days = 0\n[repo.server]\nidle_days = -1\n")
	if got := cfg.EffectiveIdleDays("server"); got != 3 {
		t.Fatalf("EffectiveIdleDays = %d, want the default 3 for non-positive values", got)
	}
}

func TestEffectiveReapTriggerInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"":                                   10 * time.Minute,
		"reap_trigger_interval = \"30m\"\n":  30 * time.Minute,
		"reap_trigger_interval = \"soon\"\n": 10 * time.Minute,
		"reap_trigger_interval = \"-5m\"\n":  10 * time.Minute,
	}
	for body, want := range cases {
		if got := load(t, body).EffectiveReapTriggerInterval(); got != want {
			t.Errorf("%q: got %s, want %s", body, got, want)
		}
	}
}

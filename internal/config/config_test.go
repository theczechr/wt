package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadParsesReposAndExpandsTilde(t *testing.T) {
	p := writeTemp(t, `
scan_roots = ["~/code"]

[repo.backend]
env = [".env", ".env.prod"]
submodules = ["corelib"]
post_create = ["deno install"]

[repo.frontend]
env = [".env", ".env.local"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "code")
	if len(cfg.ScanRoots) != 1 || cfg.ScanRoots[0] != want {
		t.Errorf("ScanRoots = %v, want [%s]", cfg.ScanRoots, want)
	}
	srv, ok := cfg.Repos["backend"]
	if !ok {
		t.Fatal("missing repo server")
	}
	if srv.Name != "backend" {
		t.Errorf("Name = %q, want server", srv.Name)
	}
	if len(srv.Env) != 2 || srv.Env[0] != ".env" {
		t.Errorf("Env = %v", srv.Env)
	}
	if len(srv.Submodules) != 1 || srv.Submodules[0] != "corelib" {
		t.Errorf("Submodules = %v", srv.Submodules)
	}
	if len(cfg.Repos["frontend"].Env) != 2 {
		t.Errorf("web Env = %v", cfg.Repos["frontend"].Env)
	}
}

func TestLoadMissingFileReturnsEmptyConfigNotError(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("missing config must not error, got %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("expected no repos, got %v", cfg.Repos)
	}
}

// TestEffectiveDeleteModeDefaultsToSoft covers the spec's central safety
// requirement: an absent delete_mode key, and any value other than the
// literal "hard" (a typo, an old/future value this build doesn't
// recognise), must default to "soft" -- never silently escalate to the
// irreversible mode.
func TestEffectiveDeleteModeDefaultsToSoft(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want string
	}{
		{"absent key", "", "soft"},
		{"explicit soft", "soft", "soft"},
		{"explicit hard", "hard", "hard"},
		{"unrecognised value", "HARD", "soft"},
		{"unrecognised value 2", "wipe", "soft"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{DeleteMode: c.mode}
			if got := cfg.EffectiveDeleteMode(); got != c.want {
				t.Errorf("EffectiveDeleteMode() with DeleteMode=%q = %q, want %q", c.mode, got, c.want)
			}
		})
	}
}

// TestEffectiveTrashRetentionDefaultsTo30Days covers the same
// absent-key-defaults-safely contract for the retention window.
func TestEffectiveTrashRetentionDefaultsTo30Days(t *testing.T) {
	cases := []struct {
		name string
		days int
		want time.Duration
	}{
		{"absent (zero)", 0, 30 * 24 * time.Hour},
		{"negative", -5, 30 * 24 * time.Hour},
		{"explicit 7", 7, 7 * 24 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{TrashRetentionDays: c.days}
			if got := cfg.EffectiveTrashRetention(); got != c.want {
				t.Errorf("EffectiveTrashRetention() with TrashRetentionDays=%d = %v, want %v", c.days, got, c.want)
			}
		})
	}
}

func TestLoadParsesDeleteModeAndRetention(t *testing.T) {
	p := writeTemp(t, `
scan_roots = ["~/code"]
delete_mode = "hard"
trash_retention_days = 14
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EffectiveDeleteMode() != "hard" {
		t.Errorf("EffectiveDeleteMode() = %q, want hard", cfg.EffectiveDeleteMode())
	}
	if want := 14 * 24 * time.Hour; cfg.EffectiveTrashRetention() != want {
		t.Errorf("EffectiveTrashRetention() = %v, want %v", cfg.EffectiveTrashRetention(), want)
	}
}

func TestRepoDefaultsWhenKeysAbsent(t *testing.T) {
	var r Repo
	if got := r.EphemeralDirOrDefault(); got != ".worktrees" {
		t.Errorf("EphemeralDirOrDefault() = %q, want .worktrees", got)
	}
	if got := r.RemoteOrDefault(); got != "origin" {
		t.Errorf("RemoteOrDefault() = %q, want origin", got)
	}
	if !r.EphemeralEnabled() {
		t.Error("ephemeral must default to enabled when the key is absent")
	}
}

func TestEphemeralPostCreateEmptyMeansRunNothing(t *testing.T) {
	body := `
[repo.backend]
post_create = ["yarn install"]
ephemeral_post_create = []
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Repos["backend"].ForEphemeral()
	if len(got.PostCreate) != 0 {
		t.Errorf("PostCreate = %v, want empty: an explicit [] means run nothing", got.PostCreate)
	}
}

func TestEphemeralPostCreateAbsentFallsBackToPostCreate(t *testing.T) {
	body := `
[repo.backend]
post_create = ["yarn install"]
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Repos["backend"].ForEphemeral()
	if len(got.PostCreate) != 1 || got.PostCreate[0] != "yarn install" {
		t.Errorf("PostCreate = %v, want [yarn install]", got.PostCreate)
	}
}

func TestEphemeralFalseDisablesRepo(t *testing.T) {
	body := "[repo.backend]\nephemeral = false\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos["backend"].EphemeralEnabled() {
		t.Error("ephemeral = false must disable the repo")
	}
}

func TestLoadRejectsEscapingEphemeralDir(t *testing.T) {
	// "." and "./" escape nothing, and that is exactly why they belong here:
	// they make the ephemeral root equal the primary checkout, so the reap
	// predicate's containment clause passes for every directory under the
	// repo. The spec calls containment an independent second gate; these
	// values collapse it to nothing while looking harmless.
	for _, dir := range []string{"../evil", "/tmp/evil", "a/../../b", ".", "./", "a/.."} {
		body := "[repo.backend]\nephemeral_dir = " + strconv.Quote(dir) + "\n"
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("ephemeral_dir = %q must be rejected", dir)
		}
	}
}

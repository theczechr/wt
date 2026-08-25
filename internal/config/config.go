// Package config loads wt's TOML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultEphemeralDir and DefaultRemote are the values used when a repo
// block omits the corresponding key.
const (
	DefaultEphemeralDir = ".worktrees"
	DefaultRemote       = "origin"
)

// Repo describes how to bootstrap one repository's worktrees.
type Repo struct {
	Name       string   `toml:"-"`
	Env        []string `toml:"env"`
	Copy       []string `toml:"copy"`
	Submodules []string `toml:"submodules"`
	PostCreate []string `toml:"post_create"`

	EphemeralDir string `toml:"ephemeral_dir"`
	// Nil means "key absent, fall back to PostCreate"; an explicitly empty
	// list means "run nothing". TOML gives no other way to tell those apart,
	// which is why this is not compared with len() anywhere.
	EphemeralPostCreate []string `toml:"ephemeral_post_create"`
	DefaultRemote       string   `toml:"default_remote"`
	Ephemeral           *bool    `toml:"ephemeral"`
}

// Config is the whole of ~/.config/wt/config.toml.
//
// DeleteMode and TrashRetentionDays live at the top level, not on Repo:
// unlike env/copy/submodules/post_create (which genuinely differ per
// project), deletion behaviour is a single blanket preference the user sets
// once for their whole fleet -- see EffectiveDeleteMode.
type Config struct {
	ScanRoots []string        `toml:"scan_roots"`
	Repos     map[string]Repo `toml:"repo"`

	// DeleteMode is "soft" (default) or "hard". See EffectiveDeleteMode.
	DeleteMode string `toml:"delete_mode"`
	// TrashRetentionDays is how long a soft-deleted worktree's manifest
	// entry survives before being purged on the next launch. See
	// EffectiveTrashRetention.
	TrashRetentionDays int `toml:"trash_retention_days"`

	// HerdrStartupDashboard controls whether the dashboard opens by itself
	// when herdr starts. See EffectiveHerdrStartupDashboard.
	HerdrStartupDashboard string `toml:"herdr_startup_dashboard"`
}

// defaultTrashRetentionDays applies whenever trash_retention_days is absent
// or non-positive.
const defaultTrashRetentionDays = 30

// EffectiveDeleteMode returns the configured deletion mode, "soft" or
// "hard". Anything other than the literal string "hard" -- including an
// absent key, a typo, or any future value this version of wt doesn't know
// about -- defaults to "soft". Deletion is this codebase's most dangerous
// feature; an unrecognised config value must never be interpreted as
// silently escalating to the irreversible mode.
func (c Config) EffectiveDeleteMode() string {
	if c.DeleteMode == "hard" {
		return "hard"
	}
	return "soft"
}

// EffectiveTrashRetention returns how long a soft-deleted worktree's
// manifest entry survives before trash.PurgeExpired drops it.
func (c Config) EffectiveTrashRetention() time.Duration {
	days := c.TrashRetentionDays
	if days <= 0 {
		days = defaultTrashRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// Startup-dashboard modes for herdr_startup_dashboard.
const (
	// StartupAuto opens the dashboard only when herdr has nothing else to
	// show. See EffectiveHerdrStartupDashboard.
	StartupAuto = "auto"
	// StartupAlways opens it on every herdr start.
	StartupAlways = "always"
	// StartupNever disables it.
	StartupNever = "never"
)

// EffectiveHerdrStartupDashboard returns the startup-dashboard mode, one of
// "auto" (default), "always" or "never".
//
// "auto" exists because herdr is not an empty editor. LazyVim shows its
// dashboard when nvim opens with no file, which is nearly always; herdr
// restores your previous session before any startup hook runs, so it usually
// opens with agents already working. Popping an overlay over that on every
// start -- including every `herdr update --handoff` -- would be noise, so
// auto shows the dashboard only when there is nothing running to interrupt.
//
// An unrecognised value falls back to "auto" rather than failing: this is a
// convenience, and a typo in it must not stop wt from loading a config whose
// remaining keys govern worktree deletion.
func (c Config) EffectiveHerdrStartupDashboard() string {
	switch c.HerdrStartupDashboard {
	case StartupAlways, StartupNever:
		return c.HerdrStartupDashboard
	default:
		return StartupAuto
	}
}

// EphemeralDirOrDefault is the directory, relative to the primary checkout,
// holding this repo's ephemeral worktrees.
func (r Repo) EphemeralDirOrDefault() string {
	if r.EphemeralDir == "" {
		return DefaultEphemeralDir
	}
	return r.EphemeralDir
}

// RemoteOrDefault is the remote used to resolve a branch that exists only
// upstream.
func (r Repo) RemoteOrDefault() string {
	if r.DefaultRemote == "" {
		return DefaultRemote
	}
	return r.DefaultRemote
}

// EphemeralEnabled reports whether wt may create (and therefore later reap)
// ephemeral worktrees for this repo. Absent means enabled.
func (r Repo) EphemeralEnabled() bool {
	return r.Ephemeral == nil || *r.Ephemeral
}

// ForEphemeral returns the repo config to bootstrap an ephemeral worktree
// with: identical except that post_create is replaced by
// ephemeral_post_create when that key is present. Returning a Repo rather
// than a flag keeps bootstrap.Run unaware that ephemerals exist at all.
func (r Repo) ForEphemeral() Repo {
	if r.EphemeralPostCreate != nil {
		r.PostCreate = r.EphemeralPostCreate
	}
	return r
}

// DefaultPath returns the standard config location.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "wt", "config.toml")
}

// validEphemeralDir rejects anything that would place a worktree outside the
// primary checkout. The value is user-edited TOML and reaches filepath.Join,
// which does not sandbox "..", and it later gates deletion: a dir that can
// escape would let the reap containment check authorise removing something
// outside the repo.
func validEphemeralDir(dir string) error {
	if dir == "" {
		return nil
	}
	if filepath.IsAbs(dir) {
		return fmt.Errorf("ephemeral_dir %q must be relative to the primary checkout", dir)
	}
	clean := filepath.Clean(dir)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ephemeral_dir %q escapes the primary checkout", dir)
	}
	if clean == "." {
		// "." does not escape anything, so it survives the check above, and
		// that is exactly the problem: it makes the ephemeral root equal to the
		// primary checkout, so the reap predicate's containment clause -- "is
		// this path inside the ephemeral dir" -- becomes true for every
		// directory anywhere under the repo. The spec calls containment an
		// independent second gate precisely so a bug in marker handling alone
		// cannot authorise a deletion; "." reduces that gate to nothing.
		return fmt.Errorf("ephemeral_dir %q would put ephemeral worktrees in the primary checkout itself; name a subdirectory", dir)
	}
	return nil
}

func expand(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// Load reads the config. A missing file yields an empty Config, not an error,
// so a first run works before the user has written any config.
func Load(path string) (Config, error) {
	var cfg Config
	cfg.Repos = map[string]Repo{}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := toml.Unmarshal(body, &cfg); err != nil {
		return cfg, err
	}
	for i, r := range cfg.ScanRoots {
		cfg.ScanRoots[i] = expand(r)
	}
	for name, r := range cfg.Repos {
		r.Name = name
		if err := validEphemeralDir(r.EphemeralDir); err != nil {
			return cfg, fmt.Errorf("repo %q: %w", name, err)
		}
		cfg.Repos[name] = r
	}
	return cfg, nil
}

package discover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/theczechr/wt/internal/model"
)

type prJSON struct {
	Number      int    `json:"number"`
	HeadRefName string `json:"headRefName"`
	State       string `json:"state"`
}

// ParsePRList converts `gh pr list --json` output into a branch-keyed map.
// Unparseable input yields an empty map, never an error: gh may be missing.
func ParsePRList(body []byte) map[string]model.PR {
	out := map[string]model.PR{}
	var items []prJSON
	if json.Unmarshal(body, &items) != nil {
		return out
	}
	for _, it := range items {
		out[it.HeadRefName] = model.PR{Number: it.Number, State: it.State}
	}
	return out
}

// PRsForRepo makes ONE gh call covering every branch in the repo. Never call
// this per worktree.
func PRsForRepo(ctx context.Context, repoPath string) (map[string]model.PR, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--state", "all", "--limit", "300",
		"--json", "number,headRefName,state")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return map[string]model.PR{}, err
	}
	return ParsePRList(out), nil
}

// cacheRoot is overridable in tests.
var cacheRoot = func() string {
	if v := os.Getenv("WT_CACHE_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "wt")
}

// CacheRoot exposes cacheRoot to other packages (aggregate's snapshot cache
// in particular), so every on-disk cache -- PR state, session labels, the
// workspace snapshot -- shares one root and one WT_CACHE_DIR override.
func CacheRoot() string { return cacheRoot() }

func cachePath(repoPath string) string {
	sum := sha256.Sum256([]byte(repoPath))
	return filepath.Join(cacheRoot(), hex.EncodeToString(sum[:8])+".json")
}

// WritePRCache persists PR state for a repo.
func WritePRCache(repoPath string, prs map[string]model.PR) error {
	p := cachePath(repoPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(prs)
	if err != nil {
		return err
	}
	return os.WriteFile(p, body, 0o644)
}

// CachedPRs returns cached PR state when it is younger than ttl.
func CachedPRs(repoPath string, ttl time.Duration) (map[string]model.PR, bool) {
	p := cachePath(repoPath)
	st, err := os.Stat(p)
	if err != nil || time.Since(st.ModTime()) > ttl {
		return nil, false
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	out := map[string]model.PR{}
	if json.Unmarshal(body, &out) != nil {
		return nil, false
	}
	return out, true
}

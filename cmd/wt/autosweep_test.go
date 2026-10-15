package main

import (
	"reflect"
	"testing"
)

func TestSpawnsSweep(t *testing.T) {
	for cmd, want := range map[string]bool{
		"status": true, "new": true, "open": true, "feat/x": true, "1234": true,
		"reap": false, "hook": false, "": false, "help": false, "timer": false,
	} {
		if got := spawnsSweep(cmd); got != want {
			t.Errorf("spawnsSweep(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// A wt launched from a git hook or alias inherits GIT_DIR and friends, which
// would override every `git -C <worktree>` the background sweep runs.
func TestSweepEnvScrubsGitRepositoryVariables(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/h",
		"GIT_DIR=/x/.git", "GIT_WORK_TREE=/x", "GIT_INDEX_FILE=/x/.git/index",
		"GIT_COMMON_DIR=/x/.git", "GIT_OBJECT_DIRECTORY=/x/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/y", "GIT_CONFIG_COUNT=1", "GIT_DIRTY=keep",
	}
	got := sweepEnv(in)
	want := []string{"PATH=/usr/bin", "HOME=/h", "GIT_CONFIG_COUNT=1", "GIT_DIRTY=keep"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sweepEnv = %q, want %q", got, want)
	}
}

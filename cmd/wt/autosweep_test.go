package main

import "testing"

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

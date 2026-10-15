package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/config"
	"github.com/theczechr/ephemera/internal/gittest"
)

func TestRunBranchesUnpushed(t *testing.T) {
	r := gittest.New(t)
	r.Git(t, r.Primary, "checkout", "-q", "-b", "feat/orphan")
	r.WriteCommit(t, r.Primary, "o.txt", "x")
	r.Git(t, r.Primary, "checkout", "-q", "main")
	var out, errb bytes.Buffer
	if code := runBranches(cfgFor(r, config.Repo{}), []string{"--unpushed"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := filepath.Base(r.Primary) + "\tfeat/orphan"
	if strings.TrimSpace(out.String()) != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

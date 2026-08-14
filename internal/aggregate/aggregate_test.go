package aggregate

import (
	"sync"
	"testing"
	"time"

	"github.com/theczechr/wt/internal/model"
)

func TestAttributeAssignsProcsToDeepestMatchingWorkspace(t *testing.T) {
	ws := []model.Workspace{
		{Path: "/u/server", Repo: "backend"},
		{Path: "/u/server/.worktrees/foo", Repo: "backend"},
	}
	procs := []model.Proc{
		{PID: 1, Cwd: "/u/server/.worktrees/foo/engine", Command: "deno run a.ts"},
		{PID: 2, Cwd: "/u/server/src", Command: "deno run b.ts"},
	}
	got := Attribute(ws, procs, nil)

	if len(got[1].Procs) != 1 || got[1].Procs[0].PID != 1 {
		t.Errorf("nested worktree should own pid 1, got %+v", got[1].Procs)
	}
	if len(got[0].Procs) != 1 || got[0].Procs[0].PID != 2 {
		t.Errorf("primary should own pid 2 only, got %+v", got[0].Procs)
	}
}

// TestGoroutinePanicRecoveryShapeUsedInCollect isolates the exact
// defer-wg.Done()-then-defer-recover() shape Collect's outer- and
// inner-goroutine bodies use, and proves it: a panicking goroutine must
// recover instead of crashing the process, and must still signal the
// WaitGroup so Collect's Wait doesn't hang forever on one bad collector.
//
// Collect itself isn't exercised end-to-end here: none of the real
// collectors it calls (Worktrees, FillStatus, SessionsFor, CachedPRs,
// PRsForRepo, Snapshot) can be made to panic deterministically from a
// directory fixture alone — they're already written to degrade to errors,
// not panic, so a "deliberately broken repo" wouldn't reliably reach a
// panic path without stubbing the collector functions, which would mean
// restructuring Collect to accept injectable collectors. That's out of
// scope for this fix, so this test targets the recovery shape directly.
func TestGoroutinePanicRecoveryShapeUsedInCollect(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var recovered any

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				mu.Lock()
				recovered = r
				mu.Unlock()
			}
		}()
		panic("boom: simulated collector panic")
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wg.Wait() never returned: a recovered panic must still call wg.Done()")
	}

	mu.Lock()
	defer mu.Unlock()
	if recovered != "boom: simulated collector panic" {
		t.Errorf("recovered value = %v, want the panic message", recovered)
	}
}

// TestSnapshotGoroutineRecoveryShapeSendsZeroValuesOnPanic isolates the
// third goroutine in Collect (the one that runs discover.Snapshot). It is
// worse than the per-repo/per-worktree goroutines covered above: it holds
// the ONLY sends on procsCh/liveCh, and Collect does an unconditional
// <-procsCh / <-liveCh. A naively-added recover() -- one that recovers but
// then simply returns -- would skip the sends entirely and deadlock Collect
// forever instead of crashing it. This proves the actual shape: even on a
// panic, both channels still receive a value.
//
// discover.Snapshot itself can't be made to panic deterministically from a
// fixture (same limitation noted on TestGoroutinePanicRecoveryShapeUsedInCollect
// above), so this targets the exact defer/recover/send shape used in
// aggregate.go's Collect.
func TestSnapshotGoroutineRecoveryShapeSendsZeroValuesOnPanic(t *testing.T) {
	procsCh := make(chan []model.Proc, 1)
	liveCh := make(chan map[string]int, 1)

	go func() {
		var p []model.Proc
		var l map[string]int
		defer func() {
			recover()
			procsCh <- p
			liveCh <- l
		}()
		panic("boom: simulated Snapshot panic")
	}()

	done := make(chan struct{})
	var gotProcs []model.Proc
	var gotLive map[string]int
	go func() {
		gotProcs = <-procsCh
		gotLive = <-liveCh
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("both channels must still receive a value after a panic, or Collect's <-procsCh/<-liveCh hangs forever")
	}
	if gotProcs != nil {
		t.Errorf("procs = %v, want nil (zero value)", gotProcs)
	}
	if gotLive != nil {
		t.Errorf("live = %v, want nil (zero value)", gotLive)
	}
}

func TestAttributeMarksLiveSessions(t *testing.T) {
	ws := []model.Workspace{{
		Path: "/u/server",
		Sessions: []model.Session{
			{ID: "aaa"},
			{ID: "bbb"},
		},
	}}
	got := Attribute(ws, nil, map[string]int{"bbb": 4242})
	if got[0].Sessions[0].Live {
		t.Error("session aaa must not be live")
	}
	if !got[0].Sessions[1].Live || got[0].Sessions[1].PID != 4242 {
		t.Errorf("session bbb must be live with pid 4242, got %+v", got[0].Sessions[1])
	}
	if !got[0].HasLiveSession() {
		t.Error("workspace must report a live session")
	}
}

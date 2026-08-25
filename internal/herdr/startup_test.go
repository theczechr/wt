package herdr

import (
	"context"
	"strings"
	"testing"
)

// TestBusyCountsAgentsNotPanes pins the signal the auto mode turns on.
// herdr always has at least one pane -- an empty herdr is still a terminal --
// so a pane count would report "busy" forever and the dashboard would never
// appear. An agent is the unit of work herdr manages, so it is the only
// count that distinguishes "came back to work in flight" from "starting
// fresh".
func TestBusyCountsAgentsNotPanes(t *testing.T) {
	if (Session{Agents: 0, Panes: 1}).Busy() {
		t.Error("a lone empty pane counted as busy; the dashboard would never open")
	}
	if (Session{Agents: 0, Panes: 4}).Busy() {
		t.Error("panes without agents counted as busy")
	}
	if !(Session{Agents: 1, Panes: 1}).Busy() {
		t.Error("a running agent did not count as busy; an overlay would land on live work")
	}
}

// TestSnapshotReadsBoxedShape covers the shape herdr actually returns, where
// the snapshot sits under a "snapshot" key.
func TestSnapshotReadsBoxedShape(t *testing.T) {
	fakeHerdr(t, `{"id":"cli:api","result":{"type":"session_snapshot","snapshot":{`+
		`"agents":[{"terminal_id":"t1"},{"terminal_id":"t2"}],`+
		`"panes":[{"pane_id":"p1"}],"workspaces":[{"workspace_id":"w1"}]}}}`, 0)

	s, err := Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if s.Agents != 2 || s.Panes != 1 {
		t.Errorf("got agents=%d panes=%d, want 2 and 1", s.Agents, s.Panes)
	}
	if !s.Busy() {
		t.Error("two running agents did not read as busy")
	}
}

// TestSnapshotReadsInlineShape guards the fallback. The "snapshot" boxing is
// an implementation detail of herdr's response type rather than a documented
// guarantee, so an un-boxed payload must still be counted rather than
// silently read as zero agents -- which would report "not busy" and drop an
// overlay onto live work.
func TestSnapshotReadsInlineShape(t *testing.T) {
	fakeHerdr(t, `{"id":"cli:api","result":{"agents":[{"terminal_id":"t1"}],"panes":[{"pane_id":"p1"}]}}`, 0)

	s, err := Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if s.Agents != 1 {
		t.Errorf("agents = %d, want 1", s.Agents)
	}
}

// TestSnapshotFailsRatherThanReportingIdle is the fail-closed case. A
// snapshot that cannot be read is unknown state, and unknown must not
// produce the zero value that means "nothing running" -- that is the value
// which authorises opening an overlay across whatever the user is doing.
func TestSnapshotFailsRatherThanReportingIdle(t *testing.T) {
	fakeHerdr(t, `{"id":"cli:api","error":{"code":"unavailable","message":"server not ready"}}`, 1)

	if _, err := Snapshot(context.Background()); err == nil {
		t.Fatal("an unreadable snapshot returned success; the caller would treat it as idle")
	} else if !strings.Contains(err.Error(), "server not ready") {
		t.Errorf("herdr's own message was lost: %v", err)
	}
}

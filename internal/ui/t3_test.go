package ui

import (
	"strings"
	"testing"

	"github.com/theczechr/ephemera/internal/model"
)

func TestResumeSkipsT3SessionsForNewestTerminalOne(t *testing.T) {
	m := withSessions(2) // "a" is newer than "b"
	m.ws[0].Sessions[0].Entrypoint = model.EntrypointSDK
	m = press(m, "r")
	if m.action != ActionResume || m.session != "b" {
		t.Fatalf("action=%v session=%q, want resume of the terminal session b", m.action, m.session)
	}
}

func TestResumeRefusesWhenOnlyT3Sessions(t *testing.T) {
	m := withSessions(1)
	m.ws[0].Sessions[0].Entrypoint = model.EntrypointSDK
	m = press(m, "r")
	if m.action == ActionResume {
		t.Fatal("resumed a T3 Code session from the terminal")
	}
	if !strings.Contains(m.status, "T3 Code") {
		t.Errorf("status = %q, want it to point at T3 Code", m.status)
	}
}

func TestResumeSelectedRefusesT3Session(t *testing.T) {
	m := withSessions(1)
	m.ws[0].Sessions[0].Entrypoint = model.EntrypointSDK
	m = press(m, "tab", "enter")
	if m.action == ActionResume {
		t.Fatal("resumed a T3 Code session from the sessions pane")
	}
}

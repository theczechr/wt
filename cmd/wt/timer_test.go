package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestLaunchdPlistRunsAnHourlySweep(t *testing.T) {
	p := launchdPlist("/Users/a b/.local/bin/wt")
	for _, want := range []string{
		"<string>" + timerLabel + "</string>",
		"<string>/Users/a b/.local/bin/wt</string>",
		"<string>reap</string>", "<string>--all</string>", "<string>--quiet</string>",
		"<key>StartInterval</key>", "<integer>3600</integer>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if err := xml.Unmarshal([]byte(p), new(struct{})); err != nil {
		t.Errorf("plist is not well-formed XML: %v", err)
	}
}

func TestLaunchdPlistEscapesThePath(t *testing.T) {
	if p := launchdPlist("/tmp/a&b/wt"); !strings.Contains(p, "/tmp/a&amp;b/wt") {
		t.Errorf("an & in the path must be escaped:\n%s", p)
	}
}

func TestSystemdUnits(t *testing.T) {
	svc := systemdService("/home/a/.local/bin/wt")
	if !strings.Contains(svc, `ExecStart="/home/a/.local/bin/wt" reap --all --quiet`) || !strings.Contains(svc, "Type=oneshot") {
		t.Errorf("service:\n%s", svc)
	}
	tmr := systemdTimer()
	if !strings.Contains(tmr, "OnUnitActiveSec=1h") || !strings.Contains(tmr, "WantedBy=timers.target") {
		t.Errorf("timer:\n%s", tmr)
	}
}

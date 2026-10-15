package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdPlistRunsAnHourlySweep(t *testing.T) {
	p := launchdPlist("/Users/a b/.local/bin/wt", "/opt/homebrew/bin:/usr/bin")
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
	if p := launchdPlist("/tmp/a&b/wt", "/usr/bin"); !strings.Contains(p, "/tmp/a&amp;b/wt") {
		t.Errorf("an & in the path must be escaped:\n%s", p)
	}
}

func TestSystemdUnits(t *testing.T) {
	svc := systemdService("/home/a/.local/bin/wt", "/usr/bin")
	if !strings.Contains(svc, `ExecStart="/home/a/.local/bin/wt" reap --all --quiet`) || !strings.Contains(svc, "Type=oneshot") {
		t.Errorf("service:\n%s", svc)
	}
	tmr := systemdTimer()
	if !strings.Contains(tmr, "OnUnitActiveSec=1h") || !strings.Contains(tmr, "WantedBy=timers.target") {
		t.Errorf("timer:\n%s", tmr)
	}
}

func TestWriteLaunchAgentWritesThePlist(t *testing.T) {
	plist := filepath.Join(t.TempDir(), "LaunchAgents", timerLabel+".plist")
	if err := writeLaunchAgent(plist, "/usr/local/bin/wt", "/usr/bin"); err != nil {
		t.Fatalf("writeLaunchAgent: %v", err)
	}
	got, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != launchdPlist("/usr/local/bin/wt", "/usr/bin") {
		t.Errorf("written plist differs from launchdPlist:\n%s", got)
	}
}

func TestWriteLaunchAgentReportsMkdirFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeLaunchAgent(filepath.Join(blocker, "LaunchAgents", timerLabel+".plist"), "/wt", "/usr/bin"); err == nil {
		t.Error("expected an error when the LaunchAgents directory cannot be created")
	}
}

func TestWriteLaunchAgentReportsWriteFailure(t *testing.T) {
	target := filepath.Join(t.TempDir(), timerLabel+".plist")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLaunchAgent(target, "/wt", "/usr/bin"); err == nil {
		t.Error("expected an error when the plist path is a directory")
	}
}

func TestSystemdServiceEscapesExecPath(t *testing.T) {
	svc := systemdService(`/tmp/a$b%c"d\e`, "/usr/bin")
	want := `ExecStart="/tmp/a$$b%%c\"d\\e" reap --all --quiet`
	if !strings.Contains(svc, want) {
		t.Errorf("service ExecStart not escaped; want %q in:\n%s", want, svc)
	}
}

// launchd starts jobs with a minimal PATH that lacks /opt/homebrew/bin, so
// the sweep could not find gh. The PATH captured at install is written in.
func TestLaunchdPlistSetsTheInstallPATH(t *testing.T) {
	p := launchdPlist("/usr/local/bin/wt", "/opt/home<brew>/bin:/a&b")
	for _, want := range []string{
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key>",
		"<string>/opt/home&lt;brew&gt;/bin:/a&amp;b</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if err := xml.Unmarshal([]byte(p), new(struct{})); err != nil {
		t.Errorf("plist is not well-formed XML: %v", err)
	}
}

// systemd's Environment= expands %-specifiers and C escapes but, unlike
// ExecStart=, not $ -- so $ is kept literal while %, \ and " are escaped.
func TestSystemdServiceSetsTheInstallPATH(t *testing.T) {
	svc := systemdService("/usr/local/bin/wt", `/opt/a$b%c"d\e:/usr/bin`)
	want := `Environment="PATH=/opt/a$b%%c\"d\\e:/usr/bin"`
	if !strings.Contains(svc, want) {
		t.Errorf("service missing %q:\n%s", want, svc)
	}
}

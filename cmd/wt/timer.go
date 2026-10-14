package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

const (
	timerLabel = "com.theczechr.ephemera.reap"
	timerUnit  = "wt-reap"
	timerUsage = "usage: wt timer install|uninstall|status"
)

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// launchdPlist is the LaunchAgent that runs an hourly sweep (spec §3.3).
func launchdPlist(exe string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + timerLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlEscape(exe) + `</string>
    <string>reap</string>
    <string>--all</string>
    <string>--quiet</string>
  </array>
  <key>StartInterval</key>
  <integer>3600</integer>
  <key>RunAtLoad</key>
  <false/>
</dict>
</plist>
`
}

func systemdService(exe string) string {
	return "[Unit]\nDescription=wt: reap finished ephemeral worktrees\n\n" +
		"[Service]\nType=oneshot\nExecStart=\"" + exe + "\" reap --all --quiet\n"
}

func systemdTimer() string {
	return "[Unit]\nDescription=Hourly wt reap\n\n" +
		"[Timer]\nOnBootSec=15min\nOnUnitActiveSec=1h\n\n" +
		"[Install]\nWantedBy=timers.target\n"
}

func run(stderr io.Writer, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stderr = stderr
	return c.Run()
}

// runTimer manages the optional hourly sweep. macOS uses a LaunchAgent,
// Linux a systemd user timer; anything else is refused.
func runTimer(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, timerUsage)
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "wt: timer:", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "wt: timer:", err)
		return 1
	}
	switch runtime.GOOS {
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", timerLabel+".plist")
		domain := "gui/" + strconv.Itoa(os.Getuid())
		switch args[0] {
		case "install":
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err == nil {
				err = os.WriteFile(plist, []byte(launchdPlist(exe)), 0o644)
			}
			if err != nil {
				fmt.Fprintln(stderr, "wt: timer:", err)
				return 1
			}
			_ = exec.Command("launchctl", "bootout", domain+"/"+timerLabel).Run() // reinstall is idempotent
			if err := run(stderr, "launchctl", "bootstrap", domain, plist); err != nil {
				fmt.Fprintln(stderr, "wt: timer: launchctl bootstrap:", err)
				return 1
			}
			fmt.Fprintln(stdout, "installed: hourly `wt reap --all` via", plist)
		case "uninstall":
			_ = exec.Command("launchctl", "bootout", domain+"/"+timerLabel).Run()
			if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
				fmt.Fprintln(stderr, "wt: timer:", err)
				return 1
			}
			fmt.Fprintln(stdout, "uninstalled")
		case "status":
			if exec.Command("launchctl", "print", domain+"/"+timerLabel).Run() == nil {
				fmt.Fprintln(stdout, "installed:", plist)
			} else {
				fmt.Fprintln(stdout, "not installed")
			}
		default:
			fmt.Fprintln(stderr, timerUsage)
			return 2
		}
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		svc, tmr := filepath.Join(dir, timerUnit+".service"), filepath.Join(dir, timerUnit+".timer")
		switch args[0] {
		case "install":
			err := os.MkdirAll(dir, 0o755)
			if err == nil {
				err = os.WriteFile(svc, []byte(systemdService(exe)), 0o644)
			}
			if err == nil {
				err = os.WriteFile(tmr, []byte(systemdTimer()), 0o644)
			}
			if err == nil {
				err = run(stderr, "systemctl", "--user", "daemon-reload")
			}
			if err == nil {
				err = run(stderr, "systemctl", "--user", "enable", "--now", timerUnit+".timer")
			}
			if err != nil {
				fmt.Fprintln(stderr, "wt: timer:", err)
				return 1
			}
			fmt.Fprintln(stdout, "installed: hourly `wt reap --all` via", tmr)
		case "uninstall":
			_ = exec.Command("systemctl", "--user", "disable", "--now", timerUnit+".timer").Run()
			for _, p := range []string{svc, tmr} {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					fmt.Fprintln(stderr, "wt: timer:", err)
					return 1
				}
			}
			_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
			fmt.Fprintln(stdout, "uninstalled")
		case "status":
			if exec.Command("systemctl", "--user", "is-enabled", "--quiet", timerUnit+".timer").Run() == nil {
				fmt.Fprintln(stdout, "installed:", tmr)
			} else {
				fmt.Fprintln(stdout, "not installed")
			}
		default:
			fmt.Fprintln(stderr, timerUsage)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "wt: timer: %s is not supported; run `wt reap --all` from your own scheduler\n", runtime.GOOS)
		return 1
	}
	return 0
}

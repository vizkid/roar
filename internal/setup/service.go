package setup

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ServiceLabel names roard to launchd; the systemd unit is roard.service.
const ServiceLabel = "roar.roard"

// ServiceSpec describes the roard service for one home.
type ServiceSpec struct {
	Home         string
	Roar         string // absolute path of the roar binary
	RexDir       string // directory of the rex binary, put first on PATH
	GOOS         string
	UID          int
	RestartDelay time.Duration
	Log          string // where launchd sends roard's stderr
}

// ServiceManager runs launchctl or systemctl.
type ServiceManager func(args ...string) error

// servicePath is where the service file lives.
func (s ServiceSpec) servicePath() string {
	if s.GOOS == "darwin" {
		return filepath.Join(s.Home, "Library", "LaunchAgents", ServiceLabel+".plist")
	}
	return filepath.Join(s.Home, ".config", "systemd", "user", "roard.service")
}

func (s ServiceSpec) path() string {
	dirs := []string{filepath.Join(s.Home, ".local", "bin"), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin"}
	if s.RexDir != "" {
		dirs = append([]string{s.RexDir}, dirs...)
	}
	return strings.Join(dirs, ":")
}

func xmlText(v string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(v))
	return b.String()
}

func (s ServiceSpec) plist() []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>daemon</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>%d</integer>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key><string>%s</string>
		<key>PATH</key><string>%s</string>
	</dict>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, ServiceLabel, xmlText(s.Roar), int(s.RestartDelay.Seconds()), xmlText(s.Home), xmlText(s.path()), xmlText(s.Log)))
}

func (s ServiceSpec) unit() []byte {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Roar service (roard): watches held messages and answers other hosts\n")
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.servicePath()), "rex.service")); err == nil {
		b.WriteString("After=rex.service\nWants=rex.service\n")
	}
	fmt.Fprintf(&b, "\n[Service]\nExecStart=%s daemon\nRestart=always\nRestartSec=%d\nEnvironment=%s\n",
		strconv.Quote(s.Roar), int(s.RestartDelay.Seconds()), strconv.Quote("PATH="+s.path()))
	b.WriteString("\n[Install]\nWantedBy=default.target\n")
	return []byte(b.String())
}

// Service writes roard's service file, loads it if it changed, and restarts
// roard, so a replaced binary takes over. It reports "written" or "ok".
func Service(s ServiceSpec, run ServiceManager) (string, error) {
	path := s.servicePath()
	want := s.unit()
	if s.GOOS == "darwin" {
		want = s.plist()
	}
	have, _ := os.ReadFile(path)
	changed := !bytes.Equal(have, want)
	if changed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if s.Log != "" {
			os.MkdirAll(filepath.Dir(s.Log), 0o700)
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, want, 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, path); err != nil {
			return "", err
		}
	}
	if s.GOOS == "darwin" {
		domain := "gui/" + strconv.Itoa(s.UID)
		if changed {
			run("launchctl", "bootout", domain+"/"+ServiceLabel) // absent the first time
			if err := run("launchctl", "bootstrap", domain, path); err != nil {
				return "", fmt.Errorf("launchctl bootstrap: %w", err)
			}
		}
		if err := run("launchctl", "kickstart", "-k", domain+"/"+ServiceLabel); err != nil {
			return "", fmt.Errorf("launchctl kickstart: %w", err)
		}
	} else {
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "roard.service"}, {"restart", "roard.service"}} {
			if !changed && args[0] != "restart" {
				continue
			}
			if err := run(append([]string{"systemctl", "--user"}, args...)...); err != nil {
				return "", fmt.Errorf("systemctl %s: %w", args[0], err)
			}
		}
	}
	if changed {
		return "written", nil
	}
	return "ok", nil
}

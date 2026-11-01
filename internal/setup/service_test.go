package setup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type calls struct{ got [][]string }

func (c *calls) run(args ...string) error {
	c.got = append(c.got, args)
	return nil
}

func (c *calls) has(prefix ...string) bool {
	return slices.ContainsFunc(c.got, func(a []string) bool { return len(a) >= len(prefix) && slices.Equal(a[:len(prefix)], prefix) })
}

func spec(t *testing.T, goos string) ServiceSpec {
	home := t.TempDir()
	return ServiceSpec{Home: home, Roar: "/opt/a&b/roar", RexDir: "/opt/rex", GOOS: goos, UID: 501,
		RestartDelay: 2 * time.Second, Log: filepath.Join(home, ".local", "state", "roar", "roard.err")}
}

// 57: setup writes the launchd agent and starts it; a second run is ok.
func TestServiceDarwin(t *testing.T) {
	s := spec(t, "darwin")
	var c calls
	got, err := Service(s, c.run)
	if err != nil || got != "written" {
		t.Fatalf("first run %q: %v", got, err)
	}
	b, err := os.ReadFile(filepath.Join(s.Home, "Library", "LaunchAgents", "roar.roard.plist"))
	if err != nil {
		t.Fatal(err)
	}
	plist := string(b)
	for _, want := range []string{"<string>roar.roard</string>", "<string>/opt/a&amp;b/roar</string>", "<string>daemon</string>",
		"<key>KeepAlive</key>", "<integer>2</integer>", "/opt/rex:"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist lacks %q:\n%s", want, plist)
		}
	}
	if !c.has("launchctl", "bootstrap", "gui/501") || !c.has("launchctl", "kickstart", "-k", "gui/501/roar.roard") {
		t.Fatalf("calls %v", c.got)
	}
	var again calls
	if got, err := Service(s, again.run); err != nil || got != "ok" {
		t.Fatalf("second run %q: %v", got, err)
	}
	if again.has("launchctl", "bootstrap") || !again.has("launchctl", "kickstart", "-k", "gui/501/roar.roard") {
		t.Fatalf("second run calls %v: want only a restart, for a replaced binary", again.got)
	}
}

func TestServiceLinux(t *testing.T) {
	s := spec(t, "linux")
	unitDir := filepath.Join(s.Home, ".config", "systemd", "user")
	os.MkdirAll(unitDir, 0o755)
	os.WriteFile(filepath.Join(unitDir, "rex.service"), []byte("[Unit]\n"), 0o644)
	var c calls
	if got, err := Service(s, c.run); err != nil || got != "written" {
		t.Fatalf("%q: %v", got, err)
	}
	b, _ := os.ReadFile(filepath.Join(unitDir, "roard.service"))
	unit := string(b)
	for _, want := range []string{`ExecStart="/opt/a&b/roar" daemon`, "Restart=always", "RestartSec=2", "After=rex.service", "Wants=rex.service", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
	if !c.has("systemctl", "--user", "daemon-reload") || !c.has("systemctl", "--user", "enable", "roard.service") ||
		!c.has("systemctl", "--user", "restart", "roard.service") {
		t.Fatalf("calls %v", c.got)
	}
}

func TestServiceLinuxWithoutRexUnit(t *testing.T) {
	s := spec(t, "linux")
	var c calls
	Service(s, c.run)
	b, _ := os.ReadFile(filepath.Join(s.Home, ".config", "systemd", "user", "roard.service"))
	if strings.Contains(string(b), "rex.service") {
		t.Fatalf("unit names a rex.service that does not exist:\n%s", b)
	}
}

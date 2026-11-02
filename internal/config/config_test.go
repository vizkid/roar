package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, file string, o Options) (*Settings, []string) {
	t.Helper()
	if file != "" {
		o.Path = filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(o.Path, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if o.Path == "" {
		o.Path = filepath.Join(t.TempDir(), "missing.toml")
	}
	s, warn, err := Load(o)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return s, warn
}

// 60: no file means every key has its default, from "default".
func TestDefaultsWithoutFile(t *testing.T) {
	s, warn := load(t, "", Options{})
	if len(warn) != 0 {
		t.Fatalf("warnings %v", warn)
	}
	if got := s.Duration("sender.call.timeout"); got != 10*time.Second {
		t.Fatalf("sender.call.timeout %v", got)
	}
	if got := s.Size("sender.upload.part_size"); got != 768<<10 {
		t.Fatalf("part_size %d", got)
	}
	for _, k := range Keys() {
		if src := s.Source(k.Name); src != "default" {
			t.Fatalf("%s source %q", k.Name, src)
		}
	}
}

// 61: profile < file < host section (that host only) < ROAR_SET.
func TestPrecedence(t *testing.T) {
	file := `
profile = "slow"
"sender.call.rtt_factor" = 10

[hosts.Baroni]
"sender.call.rtt_factor" = 20

[hosts.colonelk]
profile = "train"

[profiles.train]
"sender.stall_timeout" = "5m"
"roard.workers" = 9
`
	s, _ := load(t, file, Options{})
	if got, src := s.Duration("sender.call.timeout"), s.Source("sender.call.timeout"); got != 30*time.Second || src != "profile slow" {
		t.Fatalf("profile: %v from %q", got, src)
	}
	if got, src := s.Float("sender.call.rtt_factor"), s.Source("sender.call.rtt_factor"); got != 10 || src != "file" {
		t.Fatalf("file: %v from %q", got, src)
	}

	b, _ := load(t, file, Options{Host: "Baroni"})
	if got, src := b.Float("sender.call.rtt_factor"), b.Source("sender.call.rtt_factor"); got != 20 || src != "host Baroni" {
		t.Fatalf("host: %v from %q", got, src)
	}

	c, _ := load(t, file, Options{Host: "colonelk"})
	if got, src := c.Duration("sender.stall_timeout"), c.Source("sender.stall_timeout"); got != 5*time.Minute || src != "host colonelk profile train" {
		t.Fatalf("host profile: %v from %q", got, src)
	}
	// A host profile changes only keys that apply per host.
	if got := c.Int("roard.workers"); got != 4 {
		t.Fatalf("host profile leaked roard.workers=%d", got)
	}

	e, _ := load(t, file, Options{Host: "Baroni", Set: "sender.call.rtt_factor=3, sender.print_deadline=1m"})
	if got, src := e.Float("sender.call.rtt_factor"), e.Source("sender.call.rtt_factor"); got != 3 || src != "ROAR_SET" {
		t.Fatalf("ROAR_SET: %v from %q", got, src)
	}
	if got := e.Duration("sender.print_deadline"); got != time.Minute {
		t.Fatalf("second ROAR_SET pair %v", got)
	}
}

// ROAR_PROFILE wins over the file's profile.
func TestProfileFromEnvironment(t *testing.T) {
	s, _ := load(t, `profile = "default"`, Options{Profile: "slow"})
	if got := s.Duration("sender.stall_timeout"); got != 3*time.Minute {
		t.Fatalf("stall_timeout %v", got)
	}
}

// Nested tables and quoted dotted keys mean the same thing.
func TestNestedTables(t *testing.T) {
	s, warn := load(t, "[sender.upload]\npart_size = \"256KiB\"\n[sender.transfer]\nmin_rate = \"16KiB/s\"\n", Options{})
	if len(warn) != 0 {
		t.Fatalf("warnings %v", warn)
	}
	if got := s.Size("sender.upload.part_size"); got != 256<<10 {
		t.Fatalf("part_size %d", got)
	}
	if got := s.Size("sender.transfer.min_rate"); got != 16<<10 {
		t.Fatalf("min_rate %d", got)
	}
}

// 62: an unknown key warns; a bad value is an error naming the key.
func TestUnknownKeyWarnsBadValueFails(t *testing.T) {
	_, warn := load(t, `"sender.call.nonsense" = 1`, Options{})
	if len(warn) != 1 || !strings.Contains(warn[0], "sender.call.nonsense") {
		t.Fatalf("warnings %v", warn)
	}
	for _, bad := range []string{
		`"sender.call.timeout" = "soon"`,
		`"sender.connect.jitter" = 1.5`,
		`"sender.connect.factor" = 0.5`,
		`"roard.workers" = 0`,
		`"sender.upload.part_size" = "big"`,
		`profile = "nope"`,
	} {
		p := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(p, []byte(bad), 0o600)
		if _, _, err := Load(Options{Path: p}); err == nil {
			t.Fatalf("%s: no error", bad)
		} else if key := strings.Fields(strings.Trim(bad, `"`))[0]; key != "profile" && !strings.Contains(err.Error(), strings.Trim(key, `"`)) {
			t.Fatalf("%s: error %v does not name the key", bad, err)
		}
	}
	if _, _, err := Load(Options{Path: filepath.Join(t.TempDir(), "x"), Set: "sender.call.timeout"}); err == nil {
		t.Fatal("ROAR_SET without = accepted")
	}
}

// A host section warns about keys that do not apply per host.
func TestHostSectionScope(t *testing.T) {
	s, warn := load(t, "[hosts.Baroni]\n\"roard.workers\" = 8\n", Options{Host: "Baroni"})
	if s.Int("roard.workers") != 4 || len(warn) != 1 {
		t.Fatalf("workers %d warnings %v", s.Int("roard.workers"), warn)
	}
}

// 64: with jitter 0 the delays are exact, capped, and reset_after restarts them.
func TestBackoffSequence(t *testing.T) {
	b := Backoff{Initial: time.Second, Factor: 2, Max: 5 * time.Second, Jitter: 0, ResetAfter: time.Minute}
	r := NewRetry(b, func() float64 { return 0.99 })
	now := time.Unix(0, 0)
	var got []time.Duration
	for range 5 {
		got = append(got, r.Fail(now))
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delays %v, want %v", got, want)
		}
	}
	r.Up(now)
	if d := r.Fail(now.Add(30 * time.Second)); d != 5*time.Second {
		t.Fatalf("short connection reset the backoff: %v", d)
	}
	r.Up(now)
	if d := r.Fail(now.Add(time.Minute)); d != time.Second {
		t.Fatalf("reset_after did not reset: %v", d)
	}
}

func TestBackoffJitterRange(t *testing.T) {
	b := Backoff{Initial: 4 * time.Second, Factor: 2, Max: time.Hour, Jitter: 0.5}
	for _, u := range []float64{0, 0.25, 0.999} {
		d := b.Delay(0, u)
		if d < 2*time.Second || d > 4*time.Second {
			t.Fatalf("u=%v delay %v outside [2s,4s]", u, d)
		}
	}
	if b.Delay(0, 0) != 4*time.Second || b.Delay(0, 0.999) <= 2*time.Second {
		t.Fatal("jitter is not delay*(1 - jitter*u)")
	}
}

func TestBackoffFromSettings(t *testing.T) {
	s, _ := load(t, "", Options{Profile: "slow"})
	b := s.Backoff("sender.connect")
	if b.Initial != 2*time.Second || b.Max != 2*time.Minute || b.Factor != 2 || b.Jitter != 0.5 || b.ResetAfter != 5*time.Minute {
		t.Fatalf("backoff %+v", b)
	}
}

// 65 (the rule): deadline = max(timeout, rtt_factor * srtt).
func TestDeadlineFollowsLatency(t *testing.T) {
	d := Deadline{Timeout: 10 * time.Second, RTTFactor: 8}
	if got := d.For(100 * time.Millisecond); got != 10*time.Second {
		t.Fatalf("fast link %v", got)
	}
	if got := d.For(2 * time.Second); got != 16*time.Second {
		t.Fatalf("2s link %v", got)
	}
	if got := d.For(20 * time.Second); got != 160*time.Second {
		t.Fatalf("20s spike %v: the sender's own deadline is not capped at Rex's 30s", got)
	}
}

func TestSmoothedRTT(t *testing.T) {
	r := RTT{Alpha: 0.5}
	r.Observe(time.Second)
	if r.SRTT() != time.Second {
		t.Fatalf("first sample %v", r.SRTT())
	}
	r.Observe(3 * time.Second)
	if r.SRTT() != 2*time.Second {
		t.Fatalf("second sample %v", r.SRTT())
	}
}

// Every key the spec lists exists with its default and slow value.
func TestKeyTable(t *testing.T) {
	for name, want := range map[string][2]string{
		"sender.connect.max":       {"30s", "2m"},
		"sender.transfer.min_rate": {"256KiB/s", "16KiB/s"},
		"roard.callback.give_up":   {"2m", "10m"},
		"watcher.gap_recheck":      {"30s", "30s"},
		"watcher.sweep":            {"60s", "60s"},
		"install.ready_timeout":    {"20s", "2m"},
		"claude.poll":              {"250ms", "250ms"},
		"roard.relay.retire":       {"true", "true"},
	} {
		k, ok := Lookup(name)
		if !ok {
			t.Fatalf("no key %s", name)
		}
		if k.Default != want[0] || k.SlowValue() != want[1] {
			t.Fatalf("%s: default %q slow %q", name, k.Default, k.SlowValue())
		}
	}
}

func TestPathFromEnvironment(t *testing.T) {
	if got := DefaultPath(func(k string) string {
		return map[string]string{"ROAR_CONFIG": "/x/roar.toml"}[k]
	}, "/home/u"); got != "/x/roar.toml" {
		t.Fatal(got)
	}
	if got := DefaultPath(func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": "/xdg"}[k]
	}, "/home/u"); got != "/xdg/roar/config.toml" {
		t.Fatal(got)
	}
	if got := DefaultPath(func(string) string { return "" }, "/home/u"); got != "/home/u/.config/roar/config.toml" {
		t.Fatal(got)
	}
}

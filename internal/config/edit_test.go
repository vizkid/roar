package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `# my roar settings
profile = "default" # keep it simple

"sender.call.timeout" = "20s"  # the train

[hosts.Baroni]
# Baroni is fast
"sender.upload.part_size" = "512KiB"

[profiles.train]
"sender.stall_timeout" = "5m"
`

func editFile(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(content), 0o600)
	return p
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 67: set and unset keep every other line and comment.
func TestSetKeepsComments(t *testing.T) {
	p := editFile(t, sample)
	if err := Set(p, "", "sender.call.timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "", "watcher.coalesce", "100ms"); err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "Baroni", "sender.call.rtt_factor", "12"); err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "colonelk", "sender.stall_timeout", "3m"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{"# my roar settings", "# keep it simple", "# Baroni is fast", `"sender.upload.part_size" = "512KiB"`, "[profiles.train]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q:\n%s", want, got)
		}
	}
	if strings.Count(got, `"sender.call.timeout"`) != 1 {
		t.Fatalf("timeout written twice:\n%s", got)
	}
	s, _, err := Load(Options{Path: p, Host: "Baroni"})
	if err != nil {
		t.Fatalf("edited file does not load: %v\n%s", err, got)
	}
	if s.Duration("sender.call.timeout") != 30*time.Second || s.Duration("watcher.coalesce") != 100*time.Millisecond || s.Float("sender.call.rtt_factor") != 12 {
		t.Fatalf("values not applied:\n%s", got)
	}
	c, _, _ := Load(Options{Path: p, Host: "colonelk"})
	if c.Duration("sender.stall_timeout") != 3*time.Minute {
		t.Fatalf("new host section:\n%s", got)
	}
	// watcher.coalesce goes above the first table, or TOML would put it in one.
	if strings.Index(got, "watcher.coalesce") > strings.Index(got, "[hosts.Baroni]") {
		t.Fatalf("top-level key written inside a table:\n%s", got)
	}

	if err := Unset(p, "Baroni", "sender.upload.part_size"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); strings.Contains(got, "512KiB") || !strings.Contains(got, "# Baroni is fast") {
		t.Fatalf("unset:\n%s", got)
	}
}

func TestSetRefusesBadValuesAndKeys(t *testing.T) {
	p := editFile(t, sample)
	if err := Set(p, "", "sender.call.timeout", "soon"); err == nil {
		t.Fatal("bad duration accepted")
	}
	if err := Set(p, "", "no.such.key", "1"); err == nil {
		t.Fatal("unknown key accepted")
	}
	if err := Set(p, "Baroni", "roard.workers", "8"); err == nil {
		t.Fatal("a host section accepted a key that is not per host")
	}
	if got := read(t, p); got != sample {
		t.Fatalf("a refused set changed the file:\n%s", got)
	}
}

func TestSetProfileAndMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := SetProfile(p, "", "slow"); err != nil {
		t.Fatal(err)
	}
	if err := SetProfile(p, "colonelk", "slow"); err != nil {
		t.Fatal(err)
	}
	if err := SetProfile(p, "", "nope"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	s, _, err := Load(Options{Path: p})
	if err != nil || s.Source("sender.call.timeout") != "profile slow" {
		t.Fatalf("%v %q\n%s", err, s.Source("sender.call.timeout"), read(t, p))
	}
}

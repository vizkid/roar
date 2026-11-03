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

// Review, 2026-11-03: set wrote the text it was given. "1" parses as true
// but was written as the integer 1, and the next load refused the file.
func TestSetWritesTheParsedValue(t *testing.T) {
	p := editFile(t, "")
	if err := Set(p, "", "roard.relay.retire", "1"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); !strings.Contains(got, `"roard.relay.retire" = true`) {
		t.Fatalf("wrote:\n%s", got)
	}
	s, _, err := Load(Options{Path: p})
	if err != nil || !s.Bool("roard.relay.retire") {
		t.Fatalf("load: %v", err)
	}
}

// The reader takes a key from a nested table or a dotted key, so the editor
// must find it there too. Unset used to report success and leave it set.
func TestEditFindsNestedKeys(t *testing.T) {
	for _, c := range []struct{ name, host, file string }{
		{"table", "", "[sender.call]\ntimeout = \"42s\"\n"},
		{"dotted", "", "sender.call.timeout = \"42s\"\n"},
		{"host table", "Baroni", "[hosts.Baroni.sender.call]\ntimeout = \"42s\"\n"},
		{"quoted host", "my box", "[hosts.\"my box\".sender]\ncall.timeout = \"42s\"\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := editFile(t, c.file)
			if err := Set(p, c.host, "sender.call.timeout", "7s"); err != nil {
				t.Fatal(err)
			}
			got := read(t, p)
			if strings.Contains(got, "42s") || strings.Count(got, "7s") != 1 {
				t.Fatalf("set did not replace the nested line:\n%s", got)
			}
			if err := Unset(p, c.host, "sender.call.timeout"); err != nil {
				t.Fatal(err)
			}
			s, _, err := Load(Options{Path: p, Host: c.host})
			if err != nil || s.Source("sender.call.timeout") != "default" {
				t.Fatalf("after unset: %v, source %s:\n%s", err, s.Source("sender.call.timeout"), read(t, p))
			}
		})
	}
}

// Two spellings of one key gave no rule for which wins.
func TestLoadRefusesAKeySetTwice(t *testing.T) {
	p := editFile(t, "\"sender.call.timeout\" = \"20s\"\n\n[sender.call]\ntimeout = \"42s\"\n")
	if _, _, err := Load(Options{Path: p}); err == nil || !strings.Contains(err.Error(), "sender.call.timeout") {
		t.Fatalf("err %v", err)
	}
}

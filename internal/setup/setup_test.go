package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vizkid/roar/internal/skill"
)

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func touch(t *testing.T, parts ...string) {
	t.Helper()
	p := filepath.Join(parts...)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindRoots(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	mkdir(t, home, ".claude")
	touch(t, home, ".claude-a", ".claude.json")
	mkdir(t, home, ".claude-b") // no config: not a root
	touch(t, home, ".claude-nonfx.lock")
	mkdir(t, home, ".codex")
	touch(t, home, ".codex-x", "config.toml")
	mkdir(t, home, ".codex-y")
	env := map[string]string{"CLAUDE_CONFIG_DIR": other, "CODEX_HOME": filepath.Join(home, ".codex-x")}
	got := Find(home, func(k string) string { return env[k] })
	want := Roots{
		Claude: []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude-a"), other},
		Codex:  []string{filepath.Join(home, ".codex"), filepath.Join(home, ".codex-x")},
	}
	if !slices.Equal(got.Claude, want.Claude) || !slices.Equal(got.Codex, want.Codex) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if got := Find(t.TempDir(), func(string) string { return "" }); len(got.Claude)+len(got.Codex) != 0 {
		t.Fatalf("a bare home has roots: %+v", got)
	}
}

const existingHooks = `{
  "description": "operator hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "first", "timeout": 5}]},
      {"hooks": [{"type": "command", "command": "second"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "stop"}]}
    ]
  }
}
`

func readHooks(t *testing.T, home string) (raw string, groups []string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Hooks struct {
			SessionStart []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"SessionStart"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, g := range f.Hooks.SessionStart {
		groups = append(groups, g.Hooks[0].Command)
	}
	return string(b), groups
}

func TestSetupCodexHome(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "hooks.json"), []byte(existingHooks), 0o644)
	if _, err := Codex(home, "/bin/roar"); err != nil {
		t.Fatal(err)
	}
	raw, groups := readHooks(t, home)
	// Codex keys hook trust by position, so earlier groups keep theirs.
	if want := []string{"first", "second", "/bin/roar hook codex"}; !slices.Equal(groups, want) {
		t.Fatalf("SessionStart groups %q, want %q", groups, want)
	}
	if strings.Index(raw, `"description"`) > strings.Index(raw, `"hooks"`) ||
		strings.Index(raw, `"SessionStart"`) > strings.Index(raw, `"Stop"`) {
		t.Fatalf("keys reordered:\n%s", raw)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "skills", "roar", "SKILL.md")); string(b) != skill.Content {
		t.Fatal("skill not installed")
	}

	if got, err := Codex(home, "/bin/roar"); err != nil || got != "ok" {
		t.Fatalf("second run: %q, %v", got, err)
	}
	if again, _ := readHooks(t, home); again != raw {
		t.Fatal("second run rewrote hooks.json")
	}

	// A moved binary updates its own group in place.
	if _, err := Codex(home, "/new/roar"); err != nil {
		t.Fatal(err)
	}
	if _, groups := readHooks(t, home); !slices.Equal(groups, []string{"first", "second", "/new/roar hook codex"}) {
		t.Fatalf("after a move: %q", groups)
	}
}

func TestSetupCodexHomeWithoutHooksFile(t *testing.T) {
	home := t.TempDir()
	if _, err := Codex(home, "/bin/roar"); err != nil {
		t.Fatal(err)
	}
	if _, groups := readHooks(t, home); !slices.Equal(groups, []string{"/bin/roar hook codex"}) {
		t.Fatalf("groups %q", groups)
	}
}

// fakeClaude records claude CLI calls and answers `mcp get` from state.
type fakeClaude struct {
	calls      []string
	registered string // the Command line `mcp get` reports, or "" for none
}

func (f *fakeClaude) run(dir string, args ...string) (string, error) {
	f.calls = append(f.calls, dir+": "+strings.Join(args, " "))
	switch args[1] {
	case "get":
		if f.registered == "" {
			return "", errors.New(`No MCP server named "roar"`)
		}
		return "roar:\n  Scope: User config (available in all your projects)\n  Type: stdio\n  Command: " + f.registered + "\n  Args: mcp\n", nil
	case "add-json":
		var cfg struct{ Command string }
		json.Unmarshal([]byte(args[len(args)-1]), &cfg)
		f.registered = cfg.Command
	case "remove":
		f.registered = ""
	}
	return "", nil
}

func TestSetupClaudeRoot(t *testing.T) {
	home := t.TempDir()
	def, other := mkdir(t, home, ".claude"), mkdir(t, home, ".claude-p")
	f := &fakeClaude{}
	if _, err := Claude(def, home, "/bin/roar", f.run); err != nil {
		t.Fatal(err)
	}
	// ~/.claude is Claude's default, so CLAUDE_CONFIG_DIR stays unset.
	add := `: mcp add-json --scope user roar {"type":"stdio","command":"/bin/roar","args":["mcp"]}`
	if !slices.Contains(f.calls, add) {
		t.Fatalf("calls %q", f.calls)
	}
	if b, _ := os.ReadFile(filepath.Join(def, "skills", "roar", "SKILL.md")); string(b) != skill.Content {
		t.Fatal("skill not installed")
	}

	f = &fakeClaude{registered: "/bin/roar"}
	if got, err := Claude(other, home, "/bin/roar", f.run); err != nil || !strings.HasPrefix(got, "skill written") || strings.Contains(got, "mcp") {
		t.Fatalf("registered root: %q, %v; calls %q", got, err, f.calls)
	}
	if got, _ := Claude(other, home, "/bin/roar", f.run); got != "ok" {
		t.Fatalf("second run %q", got)
	}
	for _, c := range f.calls {
		if !strings.HasPrefix(c, other+": mcp get") {
			t.Fatalf("an up-to-date root changed: %q", f.calls)
		}
	}

	f = &fakeClaude{registered: "/old/roar"}
	Claude(other, home, "/bin/roar", f.run)
	if f.registered != "/bin/roar" || !slices.Contains(f.calls, other+": mcp remove --scope user roar") {
		t.Fatalf("stale server not replaced: %q", f.calls)
	}
}

func TestSetupGoesOnPastAFailedRoot(t *testing.T) {
	home := t.TempDir()
	bad, good := mkdir(t, home, ".codex"), mkdir(t, home, ".codex-x")
	mkdir(t, bad, "hooks.json") // a directory where the file should be
	var out, errb bytes.Buffer
	err := Run(&out, &errb, home, "/bin/roar", Roots{Codex: []string{bad, good}}, (&fakeClaude{}).run)
	if err == nil {
		t.Fatal("a failed root did not fail the run")
	}
	if !strings.Contains(errb.String(), bad) || !strings.Contains(out.String(), good) {
		t.Fatalf("stdout %q\nstderr %q", out.String(), errb.String())
	}
	if _, groups := readHooks(t, good); len(groups) != 1 {
		t.Fatal("the root after the failure was skipped")
	}
}

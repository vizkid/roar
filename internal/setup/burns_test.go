package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Burns wires each account's tools from one declaration per tool. Roar's
// names its channel server, its MCP server and its Codex hook, with the
// hook command exactly as roar setup writes it into hooks.json.
func TestBurnsDeclaration(t *testing.T) {
	home := t.TempDir()
	roar := "/opt/my tools/roar"
	got, err := Burns(home, roar)
	if err != nil || got != "written" {
		t.Fatalf("first write: %q %v", got, err)
	}
	path := filepath.Join(home, ".config", "burns", "tools.d", "roar.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Claude struct {
			ChannelServers []string `json:"channel_servers"`
			MCP            []struct {
				Name, Command string
				Args          []string
			} `json:"mcp"`
		} `json:"claude"`
		Codex struct {
			Hooks []struct {
				Event, Command string
				Timeout        int
			} `json:"hooks"`
		} `json:"codex"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	c, x := d.Claude, d.Codex
	if len(c.ChannelServers) != 1 || c.ChannelServers[0] != "roar" ||
		len(c.MCP) != 1 || c.MCP[0].Name != "roar" || c.MCP[0].Command != roar || len(c.MCP[0].Args) != 1 || c.MCP[0].Args[0] != "mcp" {
		t.Fatalf("claude part %+v", c)
	}
	if len(x.Hooks) != 1 || x.Hooks[0].Event != "SessionStart" || x.Hooks[0].Command != hookCommand(roar) || x.Hooks[0].Timeout != 5 {
		t.Fatalf("codex part %+v", x)
	}
	if got, err := Burns(home, roar); err != nil || got != "ok" {
		t.Fatalf("second run: %q %v", got, err)
	}
}

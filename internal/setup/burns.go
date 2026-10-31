package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// declaration is what Roar needs in each account, for Burns to apply at
// home setup and at launch: the Claude channel server for the launch flag,
// the MCP server, and the Codex hook. Burns reads one such file per tool
// from ~/.config/burns/tools.d, so it needs no subprocess at launch.
type declaration struct {
	Claude struct {
		ChannelServers []string    `json:"channel_servers"`
		MCP            []mcpServer `json:"mcp"`
	} `json:"claude"`
	Codex struct {
		Hooks []codexHook `json:"hooks"`
	} `json:"codex"`
}

type mcpServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type codexHook struct {
	Event   string `json:"event"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// Burns writes Roar's declaration for Burns, and reports "ok" when the
// file already says the same.
func Burns(home, roar string) (string, error) {
	var d declaration
	d.Claude.ChannelServers = []string{"roar"}
	d.Claude.MCP = []mcpServer{{Name: "roar", Command: roar, Args: []string{"mcp"}}}
	d.Codex.Hooks = []codexHook{{Event: "SessionStart", Command: hookCommand(roar), Timeout: hookTimeout}}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	path := filepath.Join(home, ".config", "burns", "tools.d", "roar.json")
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return "ok", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".roar.json.*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return "written", os.Rename(tmp.Name(), path)
}

package setup

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// ClaudeCLI runs `claude` with CLAUDE_CONFIG_DIR set to dir, or unset when
// dir is "", and returns its output.
type ClaudeCLI func(dir string, args ...string) (string, error)

// Claude installs the skill in root and registers `roar mcp` as a
// user-scope MCP server, through Claude's own CLI so Roar never edits a
// config file Claude rewrites while it runs.
func Claude(root, home, roar string, cli ClaudeCLI) (string, error) {
	var done []string
	if err := installSkill(root, &done); err != nil {
		return "", err
	}
	dir := root
	if root == filepath.Join(home, ".claude") {
		dir = "" // Claude's default: its user config is ~/.claude.json
	}
	got, err := cli(dir, "mcp", "get", "roar")
	user := err == nil && strings.Contains(got, "Scope: User")
	if user && strings.Contains(got, "Command: "+roar+"\n") && strings.Contains(got, "Args: mcp\n") {
		return summary(done), nil
	}
	if user {
		if _, err := cli(dir, "mcp", "remove", "--scope", "user", "roar"); err != nil {
			return "", err
		}
	}
	cfg, _ := json.Marshal(struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{"stdio", roar, []string{"mcp"}})
	if _, err := cli(dir, "mcp", "add-json", "--scope", "user", "roar", string(cfg)); err != nil {
		return "", err
	}
	if user {
		done = append(done, "mcp updated")
	} else {
		done = append(done, "mcp added")
	}
	return summary(done), nil
}

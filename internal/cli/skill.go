package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/vizkid/roar/internal/skill"
)

// skillRoot is the config root the launched agent reads skills from. It uses
// the pane's own environment: Rex starts panes in its server's login
// environment, which may differ from the shell that ran rex new.
func skillRoot(env Env, agent string) string {
	override, dir := "CLAUDE_CONFIG_DIR", ".claude"
	if agent == "codex" {
		override, dir = "CODEX_HOME", ".codex"
	}
	if v := env.Getenv(override); v != "" {
		return v
	}
	return filepath.Join(env.Getenv("HOME"), dir)
}

// installSkill installs the roar skill for agent before it starts. Failure
// only warns: the header's reply hint still works without the skill.
func installSkill(env Env, agent string) {
	path, err := skill.Install(skillRoot(env, agent))
	switch {
	case errors.Is(err, skill.ErrOperatorManaged):
		// The operator's own copy wins; nothing to say.
	case err != nil:
		fmt.Fprintf(env.Stderr, "roar %s: skill not installed at %s: %v\n", agent, path, err)
	}
}

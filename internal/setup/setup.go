// Package setup adds Roar to the default config of every Claude and Codex
// config root in a home directory, so plain `claude` and `codex` get it
// without a wrapper.
package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vizkid/roar/internal/skill"
)

// Roots are the config roots setup changes.
type Roots struct{ Claude, Codex []string }

// Find returns the default roots (~/.claude, ~/.codex) when they exist,
// every ~/.claude-* or ~/.codex-* directory that holds the agent's config
// file, and the CLAUDE_CONFIG_DIR and CODEX_HOME overrides.
func Find(home string, getenv func(string) string) Roots {
	return Roots{
		Claude: find(home, ".claude", ".claude.json", getenv("CLAUDE_CONFIG_DIR")),
		Codex:  find(home, ".codex", "config.toml", getenv("CODEX_HOME")),
	}
}

func find(home, base, marker, override string) []string {
	var roots []string
	add := func(dir string) {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() && !slices.Contains(roots, dir) {
			roots = append(roots, dir)
		}
	}
	add(filepath.Join(home, base))
	matches, _ := filepath.Glob(filepath.Join(home, base+"-*"))
	for _, m := range matches {
		if _, err := os.Stat(filepath.Join(m, marker)); err == nil {
			add(m)
		}
	}
	if override != "" {
		add(override)
	}
	return roots
}

// Run sets up every root, printing one line per root. A failed root is
// reported and skipped; Run then returns an error.
func Run(out, errw io.Writer, home, roar string, roots Roots, cli ClaudeCLI) error {
	failed := 0
	report := func(agent, root, summary string, err error) {
		if err != nil {
			failed++
			fmt.Fprintf(errw, "%s %s: %v\n", agent, root, err)
			return
		}
		fmt.Fprintf(out, "%s %s: %s\n", agent, root, summary)
	}
	for _, r := range roots.Claude {
		s, err := Claude(r, home, roar, cli)
		report("claude", r, s, err)
	}
	for _, r := range roots.Codex {
		s, err := Codex(r, roar)
		report("codex", r, s, err)
	}
	if failed > 0 {
		return fmt.Errorf("setup failed for %d config roots", failed)
	}
	return nil
}

// installSkill adds the skill's part of a root's summary.
func installSkill(root string, done *[]string) error {
	wrote, err := skill.Install(root)
	switch {
	case errors.Is(err, skill.ErrOperatorManaged):
		*done = append(*done, "skill left to the operator")
	case err != nil:
		return err
	case wrote:
		*done = append(*done, "skill written")
	}
	return nil
}

func summary(done []string) string {
	if len(done) == 0 {
		return "ok"
	}
	return strings.Join(done, ", ")
}

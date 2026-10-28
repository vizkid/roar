// Package skill installs the roar agent skill, which teaches Claude Code and
// Codex to read Roar's header, answer with roar send --reply, and start
// messages of their own.
package skill

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Content is the SKILL.md roar setup installs.
//
//go:embed SKILL.md
var Content string

// marker identifies a SKILL.md Roar wrote and may therefore rewrite.
const marker = "generated-by: roar"

// ErrOperatorManaged means skills/roar is a symlink or a file Roar did not
// write. Roar never changes either.
var ErrOperatorManaged = errors.New("skills/roar is managed by the operator")

// Install writes the skill to root/skills/roar/SKILL.md, where root is an
// agent's config root (~/.claude, ~/.codex, or an override). It leaves an
// identical copy alone, and reports whether it wrote.
func Install(root string) (bool, error) {
	dir := filepath.Join(root, "skills", "roar")
	path := filepath.Join(dir, "SKILL.md")
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return false, ErrOperatorManaged
	}
	old, err := os.ReadFile(path)
	switch {
	case err == nil && string(old) == Content:
		return false, nil
	case err == nil && !strings.Contains(string(old), marker):
		return false, ErrOperatorManaged
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	// Write then rename, so an agent starting meanwhile never reads half a file.
	tmp, err := os.CreateTemp(dir, ".SKILL.md.*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(Content); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, fmt.Errorf("installing %s: %w", path, err)
	}
	return true, nil
}

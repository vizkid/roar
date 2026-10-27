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

// Content is the SKILL.md every launch installs.
//
//go:embed SKILL.md
var Content string

// marker identifies a SKILL.md Roar wrote and may therefore rewrite.
const marker = "generated-by: roar"

// ErrOperatorManaged means skills/roar is a symlink or a file Roar did not
// write. Roar never changes either.
var ErrOperatorManaged = errors.New("skills/roar is managed by the operator")

// Install writes the skill to root/skills/roar/SKILL.md, where root is an
// agent's config root (~/.claude, ~/.codex, or an override). It returns the
// path, and leaves an identical copy alone.
func Install(root string) (string, error) {
	dir := filepath.Join(root, "skills", "roar")
	path := filepath.Join(dir, "SKILL.md")
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return path, ErrOperatorManaged
	}
	old, err := os.ReadFile(path)
	switch {
	case err == nil && string(old) == Content:
		return path, nil
	case err == nil && !strings.Contains(string(old), marker):
		return path, ErrOperatorManaged
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return path, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, err
	}
	// Write then rename, so an agent starting meanwhile never reads half a file.
	tmp, err := os.CreateTemp(dir, ".SKILL.md.*")
	if err != nil {
		return path, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(Content); err != nil {
		tmp.Close()
		return path, err
	}
	if err := tmp.Close(); err != nil {
		return path, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return path, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return path, fmt.Errorf("installing %s: %w", path, err)
	}
	return path, nil
}

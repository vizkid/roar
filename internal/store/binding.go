package store

import (
	"path/filepath"
	"time"
)

// CodexBinding names the thread a pane's Codex TUI shows. The Codex
// SessionStart hook writes it on every thread change.
type CodexBinding struct {
	Thread    string    `json:"thread"`
	CodexHome string    `json:"codex_home"`
	PID       int       `json:"pid"` // the TUI, as Rex reports the pane's foreground
	At        time.Time `json:"at"`
}

// BindCodex records the pane's current Codex thread, replacing any earlier one.
func (s *Store) BindCodex(block string, b CodexBinding) error {
	if err := safeName(block); err != nil {
		return err
	}
	return writeJSON(s.bindingPath(block), b)
}

// CodexBinding returns the pane's last recorded binding, if any.
func (s *Store) CodexBinding(block string) (CodexBinding, bool) {
	var b CodexBinding
	if safeName(block) != nil {
		return b, false
	}
	if err := readJSON(s.bindingPath(block), &b); err != nil {
		return CodexBinding{}, false
	}
	return b, true
}

func (s *Store) bindingPath(block string) string {
	return filepath.Join(s.root, "codex", block+".json")
}

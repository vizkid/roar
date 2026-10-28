package store

import (
	"path/filepath"
	"strings"
	"time"
)

// CodexBinding names the thread a pane's Codex TUI shows. The Codex
// SessionStart hook writes it for an in-process Codex; for a daemon-backed
// one, Roar learns it from a delivered message (see cli/identity.go).
type CodexBinding struct {
	Thread    string    `json:"thread"`
	Session   string    `json:"session,omitempty"`
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

// CodexThread finds the pane whose current binding names thread.
func (s *Store) CodexThread(thread string) (block string, b CodexBinding, ok bool) {
	paths, _ := filepath.Glob(filepath.Join(s.root, "codex", "*.json"))
	for _, p := range paths {
		var c CodexBinding
		if readJSON(p, &c) == nil && c.Thread == thread && thread != "" {
			return strings.TrimSuffix(filepath.Base(p), ".json"), c, true
		}
	}
	return "", CodexBinding{}, false
}

func (s *Store) bindingPath(block string) string {
	return filepath.Join(s.root, "codex", block+".json")
}

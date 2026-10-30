package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// AgyBinding is what an Antigravity pane's own `roar` command learned from
// agy: the conversation and the local agent API credentials agy gives only
// to commands its model runs. The token can drive the session, so the file
// is 0600 and stays on this host.
type AgyBinding struct {
	Conversation string    `json:"conversation"`
	Address      string    `json:"address"`
	Token        string    `json:"token"`
	Exe          string    `json:"exe"`
	Session      string    `json:"session,omitempty"`
	PID          int       `json:"pid"` // agy, as Rex reports the pane's foreground
	At           time.Time `json:"at"`
}

// BindAgy records the pane's agy session, replacing any earlier one.
func (s *Store) BindAgy(block string, b AgyBinding) error {
	if err := safeName(block); err != nil {
		return err
	}
	return writeJSON(s.agyPath(block), b)
}

// AgyBinding returns the pane's recorded binding, if any.
func (s *Store) AgyBinding(block string) (AgyBinding, bool) {
	var b AgyBinding
	if safeName(block) != nil || readJSON(s.agyPath(block), &b) != nil {
		return AgyBinding{}, false
	}
	return b, true
}

// UnbindAgy deletes the pane's binding: its agy exited or its token failed.
func (s *Store) UnbindAgy(block string) {
	if safeName(block) == nil {
		os.Remove(s.agyPath(block))
	}
}

// PruneAgy deletes bindings whose agy process has exited, so dead tokens
// do not stay on disk when no message goes to their pane again. A reused
// PID keeps a binding; delivery still checks the pane's foreground.
func (s *Store) PruneAgy() {
	paths, _ := filepath.Glob(filepath.Join(s.root, "agy", "*.json"))
	for _, p := range paths {
		var b AgyBinding
		if readJSON(p, &b) == nil && b.PID > 0 && !alive(b.PID) {
			os.Remove(p)
		}
	}
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (s *Store) agyPath(block string) string {
	return filepath.Join(s.root, "agy", block+".json")
}

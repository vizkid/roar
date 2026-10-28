package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Retention is how long a finished message stays, so a late reply can still
// find its original.
const Retention = 7 * 24 * time.Hour

// Prune removes finished messages older than Retention, and their sent
// records. Unread messages are never pruned.
func (s *Store) Prune(now time.Time) error {
	all, err := s.All()
	if err != nil {
		return err
	}
	for _, m := range all {
		if !m.Record.State.Final() || now.Sub(m.Record.UpdatedAt) < Retention {
			continue
		}
		c, err := s.Claim(m.Envelope.ID)
		if errors.Is(err, ErrBusy) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		os.Remove(filepath.Join(s.root, "ids", m.Envelope.ID))
		err = os.RemoveAll(m.Dir)
		c.Release()
		if err != nil {
			return err
		}
	}
	sent, _ := filepath.Glob(filepath.Join(s.root, "sent", "rm-*.json"))
	for _, f := range sent {
		if info, err := os.Stat(f); err == nil && now.Sub(info.ModTime()) > Retention {
			os.Remove(f)
		}
	}
	return nil
}

// SettleOrphans moves messages whose deliverer died mid-call to unknown:
// submitting ones, whose pasted text may sit in the composer, and ones a
// Codex sender wrote, whose turn may have run. Nothing may retry them
// automatically. Claude's adapter keeps messages written while it waits for
// receipt, so its writes stay.
func (s *Store) SettleOrphans(now time.Time) (int, error) {
	all, err := s.All()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range all {
		reason := orphanReason(m.Record)
		if reason == "" {
			continue
		}
		c, err := s.Claim(m.Envelope.ID)
		if errors.Is(err, ErrBusy) {
			continue // still being typed
		}
		if err != nil {
			return n, err
		}
		if orphanReason(c.Message().Record) == reason {
			err = c.Move(Unknown, now, func(r *Record) { r.Reason = reason })
			if err == nil {
				n++
			}
		}
		c.Release()
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func orphanReason(r Record) string {
	switch {
	case r.State == Submitting:
		return "interrupted_while_typing"
	case r.State == Written && strings.HasPrefix(r.Writer, "codex:"):
		return "interrupted_while_writing"
	}
	return ""
}

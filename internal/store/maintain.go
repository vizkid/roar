package store

import (
	"errors"
	"os"
	"path/filepath"
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
	sent, _ := filepath.Glob(filepath.Join(s.root, "sent", "pm-*.json"))
	for _, f := range sent {
		if info, err := os.Stat(f); err == nil && now.Sub(info.ModTime()) > Retention {
			os.Remove(f)
		}
	}
	return nil
}

// SettleOrphans moves submitting messages whose typer died to unknown. The
// pasted text may sit in the composer, so nothing may retry automatically.
func (s *Store) SettleOrphans(now time.Time) (int, error) {
	all, err := s.All()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range all {
		if m.Record.State != Submitting {
			continue
		}
		c, err := s.Claim(m.Envelope.ID)
		if errors.Is(err, ErrBusy) {
			continue // still being typed
		}
		if err != nil {
			return n, err
		}
		if c.Message().Record.State == Submitting {
			err = c.Move(Unknown, now, func(r *Record) { r.Reason = "interrupted_while_typing" })
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

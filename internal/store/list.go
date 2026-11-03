package store

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// Inbox lists messages addressed to one block, oldest first.
func (s *Store) Inbox(block string) ([]Message, error) {
	if err := safeName(block); err != nil {
		return nil, err
	}
	return s.list(filepath.Join(s.root, "box", block, "*"))
}

// All lists every stored message, oldest first.
func (s *Store) All() ([]Message, error) {
	return s.list(filepath.Join(s.root, "box", "*", "*"))
}

// Unfinished lists one block's messages not yet in a final state, oldest
// first. Delivery loops call it every few hundred milliseconds, and finished
// messages stay for store.retention, so it reads each small state.json first
// and loads an envelope only for a message still in flight.
func (s *Store) Unfinished(block string) ([]Message, error) {
	if err := safeName(block); err != nil {
		return nil, err
	}
	return s.where(filepath.Join(s.root, "box", block, "*"), func(r Record) bool { return !r.State.Final() })
}

// allWhere lists every stored message whose record keep accepts.
func (s *Store) allWhere(keep func(Record) bool) ([]Message, error) {
	return s.where(filepath.Join(s.root, "box", "*", "*"), keep)
}

// QueuedBlocks returns the panes that have queued messages. It reads only
// each message's state.json, because the watcher calls it on every
// relevant Rex event. Delivery still loads each message through Claim.
func (s *Store) QueuedBlocks() (map[string]bool, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, "box", "*", "*", "state.json"))
	if err != nil {
		return nil, err
	}
	blocks := map[string]bool{}
	for _, p := range paths {
		var r Record
		if err := readJSON(p, &r); err != nil {
			if os.IsNotExist(err) {
				continue // pruned or moved while listing
			}
			return nil, err
		}
		if r.State == Queued {
			blocks[filepath.Base(filepath.Dir(filepath.Dir(p)))] = true
		}
	}
	return blocks, nil
}

func (s *Store) list(pattern string) ([]Message, error) { return s.where(pattern, nil) }

// where loads the messages under pattern whose record keep accepts; nil
// keeps all. Each record is read before its envelope, so a message keep
// refuses costs one small read.
func (s *Store) where(pattern string, keep func(Record) bool) ([]Message, error) {
	dirs, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, d := range dirs {
		m := Message{Dir: d}
		err := readJSON(filepath.Join(d, "state.json"), &m.Record)
		if err == nil && keep != nil && !keep(m.Record) {
			continue
		}
		if err == nil {
			err = readEnvelope(d, &m.Envelope)
		}
		if os.IsNotExist(err) || errors.Is(err, ErrNotFound) {
			continue // pruned while listing
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Envelope.ID < out[j].Envelope.ID })
	return out, nil
}

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

func (s *Store) list(pattern string) ([]Message, error) {
	dirs, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, d := range dirs {
		m, err := load(d)
		if errors.Is(err, ErrNotFound) {
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

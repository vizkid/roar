package store

import (
	"os"
	"path/filepath"
)

// NotifyDir is the directory roard watches. Every stored message and every
// state change leaves an empty file named after the message there.
// Watching one directory is cheaper than watching every message: on macOS
// kqueue opens each file of a watched directory.
func (s *Store) NotifyDir() string { return filepath.Join(s.root, "notify") }

// notify marks a message changed. It runs after the change is durable and
// never fails the change: roard also reconciles on start and on its timer.
func (s *Store) notify(id string) {
	dir := s.NotifyDir()
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, id), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		f.Close()
	}
}

// TakeNotified lists the changed messages and clears their marks. A mark is
// removed before the caller reads the record, so a change made after the
// removal leaves a new mark and is never lost.
func (s *Store) TakeNotified() ([]string, error) {
	entries, err := os.ReadDir(s.NotifyDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if safeName(e.Name()) != nil {
			continue
		}
		if err := os.Remove(filepath.Join(s.NotifyDir(), e.Name())); err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

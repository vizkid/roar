// Package store keeps messages in one directory per host. Commands share it
// through the file system; per-message file locks serialize changes.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vizkid/roar/internal/envelope"
)

var (
	ErrNotFound = errors.New("message not found")
	ErrBusy     = errors.New("held by another process")
	ErrLimit    = errors.New("limit_exceeded")
)

// Limits cap unread messages: body bytes per recipient and per host,
// attachment bytes per host, and the number of unread messages per host.
type Limits struct {
	PerRecipient int64
	PerHost      int64
	FilesPerHost int64
	Messages     int
}

// DefaultLimits match the spec: 2 MiB per recipient, 16 MiB per host.
var DefaultLimits = Limits{PerRecipient: 2 << 20, PerHost: 16 << 20, FilesPerHost: 256 << 20, Messages: 1000}

// Store is one host's message directory.
type Store struct {
	root   string
	Limits Limits
}

// Message is an envelope with its current record.
type Message struct {
	Envelope envelope.Envelope
	Record   Record
	Dir      string
}

// DefaultRoot is $ROAR_HOME, else $XDG_STATE_HOME/roar, else ~/.local/state/roar.
func DefaultRoot() (string, error) {
	if v := os.Getenv("ROAR_HOME"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "roar"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "roar"), nil
}

// Open creates the directory layout if needed.
func Open(root string) (*Store, error) {
	for _, d := range []string{"box", "ids", "sent", "adapters", "codex", "agy", "panes", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: root, Limits: DefaultLimits}, nil
}

// Root is the store directory.
func (s *Store) Root() string { return s.root }

// Put stores a new message as queued. A message with the same ID already
// stored is a duplicate, which is success.
func (s *Store) Put(e envelope.Envelope, now time.Time) (dup bool, err error) {
	return s.PutWithFiles(e, now, nil)
}

// PutWithFiles stores a message and its attachments together: the files go
// into the staging directory first, so a message never appears without them.
func (s *Store) PutWithFiles(e envelope.Envelope, now time.Time, files map[string][]byte) (dup bool, err error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	for _, f := range e.Files {
		if !f.Matches(files[f.Name]) {
			return false, fmt.Errorf("attachment %q does not match its size and digest", f.Name)
		}
	}
	if err := safeName(e.To.Block); err != nil {
		return false, err
	}
	if _, err := s.Get(e.ID); err == nil {
		return true, nil
	}
	if err := s.checkLimits(e); err != nil {
		return false, err
	}
	tmp, err := os.MkdirTemp(filepath.Join(s.root, "tmp"), e.ID+"-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	if err := writeFiles(tmp, e.Files, files); err != nil {
		return false, err
	}
	if err := writeJSON(filepath.Join(tmp, "envelope.json"), e); err != nil {
		return false, err
	}
	if err := writeJSON(filepath.Join(tmp, "state.json"), Record{State: Queued, UpdatedAt: now}); err != nil {
		return false, err
	}
	boxDir := filepath.Join(s.root, "box", e.To.Block)
	if err := os.MkdirAll(boxDir, 0o700); err != nil {
		return false, err
	}
	dir := filepath.Join(boxDir, e.ID)
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			return true, nil
		}
		return false, err
	}
	if err := syncDir(boxDir); err != nil {
		return false, err
	}
	link := filepath.Join(s.root, "ids", e.ID)
	if err := os.Symlink(filepath.Join("..", "box", e.To.Block, e.ID), link); err != nil && !os.IsExist(err) {
		return false, err
	}
	return false, nil
}

// Get loads one message by ID.
func (s *Store) Get(id string) (Message, error) {
	if !envelope.ValidID(id) {
		return Message{}, ErrNotFound
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(s.root, "ids", id))
	if err != nil {
		// The index link is written after the rename; a crash between them
		// leaves only the box entry.
		matches, _ := filepath.Glob(filepath.Join(s.root, "box", "*", id))
		if len(matches) != 1 {
			return Message{}, ErrNotFound
		}
		dir = matches[0]
	}
	m, err := load(dir)
	if err != nil {
		return m, err
	}
	// The message must live where its own ID and recipient say, under this
	// store: an index link or box entry pointing elsewhere is not it.
	box, _ := filepath.EvalSymlinks(filepath.Join(s.root, "box"))
	if m.Envelope.ID != id || filepath.Dir(filepath.Dir(dir)) != box || filepath.Base(filepath.Dir(dir)) != m.Envelope.To.Block {
		return Message{}, ErrNotFound
	}
	return m, nil
}

func fileBytes(e envelope.Envelope) int64 {
	var n int64
	for _, f := range e.Files {
		n += f.Size
	}
	return n
}

func load(dir string) (Message, error) {
	m := Message{Dir: dir}
	if err := readJSON(filepath.Join(dir, "envelope.json"), &m.Envelope); err != nil {
		if os.IsNotExist(err) {
			return m, ErrNotFound
		}
		return m, err
	}
	if err := readJSON(filepath.Join(dir, "state.json"), &m.Record); err != nil {
		return m, err
	}
	return m, nil
}

// Render is the text an agent receives, with attachment paths on this host.
func (m Message) Render() string {
	if len(m.Envelope.Files) == 0 {
		return m.Envelope.Render()
	}
	return m.Envelope.RenderIn(m.Dir)
}

func writeFiles(dir string, list []envelope.File, files map[string][]byte) error {
	if len(list) == 0 {
		return nil
	}
	fdir := filepath.Join(dir, "files")
	if err := os.Mkdir(fdir, 0o700); err != nil {
		return err
	}
	for _, f := range list {
		h, err := os.OpenFile(filepath.Join(fdir, f.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = h.Write(files[f.Name])
		if err == nil {
			err = h.Sync()
		}
		if cerr := h.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return syncDir(fdir)
}

// checkLimits is advisory across processes: two simultaneous sends can both
// pass. The limits exist to stop runaway senders, not to meter exactly.
func (s *Store) checkLimits(e envelope.Envelope) error {
	all, err := s.All()
	if err != nil {
		return err
	}
	size := int64(len(e.Text))
	host, recipient, files, count := size, size, fileBytes(e), 1
	for _, m := range all {
		if m.Record.State.Final() {
			continue
		}
		n := int64(len(m.Envelope.Text))
		host += n
		files += fileBytes(m.Envelope)
		count++
		if m.Envelope.To.Block == e.To.Block {
			recipient += n
		}
	}
	if s.Limits.FilesPerHost > 0 && files > s.Limits.FilesPerHost {
		return fmt.Errorf("%w: host would hold %d unread attachment bytes; the limit is %d", ErrLimit, files, s.Limits.FilesPerHost)
	}
	if s.Limits.Messages > 0 && count > s.Limits.Messages {
		return fmt.Errorf("%w: host would hold %d unread messages; the limit is %d", ErrLimit, count, s.Limits.Messages)
	}
	if recipient > s.Limits.PerRecipient {
		return fmt.Errorf("%w: recipient would hold %d unread bytes; the limit is %d", ErrLimit, recipient, s.Limits.PerRecipient)
	}
	if host > s.Limits.PerHost {
		return fmt.Errorf("%w: host would hold %d unread bytes; the limit is %d", ErrLimit, host, s.Limits.PerHost)
	}
	return nil
}

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vizkid/ping/internal/envelope"
)

// Claim is exclusive ownership of one message for the length of an attempt.
type Claim struct {
	lock *os.File
	msg  Message
}

// Claim locks a message. It returns ErrBusy when another process holds it.
func (s *Store) Claim(id string) (*Claim, error) {
	m, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	f, err := tryLock(filepath.Join(m.Dir, "lock"))
	if err != nil {
		return nil, err
	}
	// Reload under the lock: the record may have changed since Get.
	m, err = load(m.Dir)
	if err != nil {
		unlock(f)
		return nil, err
	}
	return &Claim{lock: f, msg: m}, nil
}

// Message is the claimed message as of the last change.
func (c *Claim) Message() Message { return c.msg }

// Move changes state. edit may set other record fields first.
func (c *Claim) Move(to State, now time.Time, edit func(*Record)) error {
	from := c.msg.Record.State
	if from != to && !CanMove(from, to) {
		return fmt.Errorf("cannot move %s from %s to %s", c.msg.Envelope.ID, from, to)
	}
	r := c.msg.Record
	if edit != nil {
		edit(&r)
	}
	r.State = to
	r.UpdatedAt = now
	if to != Queued {
		r.HoldReason = ""
	}
	if err := writeJSON(filepath.Join(c.msg.Dir, "state.json"), r); err != nil {
		return err
	}
	c.msg.Record = r
	return nil
}

// Release drops the claim.
func (c *Claim) Release() error { return unlock(c.lock) }

// Withdraw expires the sender's own message while it is still queued.
func (s *Store) Withdraw(id, fromBlock string, now time.Time) error {
	c, err := s.Claim(id)
	if err != nil {
		return err
	}
	defer c.Release()
	m := c.Message()
	if m.Envelope.From.Block != fromBlock {
		return errors.New("only the sender may withdraw a message")
	}
	if m.Record.State != Queued {
		return fmt.Errorf("message is %s; only queued messages can be withdrawn", m.Record.State)
	}
	return c.Move(Expired, now, func(r *Record) { r.Reason = "withdrawn" })
}

// AdapterLock marks a live native adapter for one block.
type AdapterLock struct{ f *os.File }

// HoldAdapter takes the adapter lock for a block. ErrBusy means another
// adapter already serves it.
func (s *Store) HoldAdapter(block string) (*AdapterLock, error) {
	if err := safeName(block); err != nil {
		return nil, err
	}
	f, err := tryLock(filepath.Join(s.root, "adapters", block+".lock"))
	if err != nil {
		return nil, err
	}
	return &AdapterLock{f: f}, nil
}

// Release gives the block back to fallback delivery.
func (l *AdapterLock) Release() error { return unlock(l.f) }

// AdapterLive reports whether a native adapter holds the block's lock.
func (s *Store) AdapterLive(block string) bool {
	l, err := s.HoldAdapter(block)
	if errors.Is(err, ErrBusy) {
		return true
	}
	if err == nil {
		l.Release()
	}
	return false
}

// RecordSent keeps routing details for the sender's `ping status`.
func (s *Store) RecordSent(e envelope.Envelope) error {
	if !envelope.ValidID(e.ID) {
		return fmt.Errorf("invalid message id %q", e.ID)
	}
	e.Text = ""
	return writeJSON(filepath.Join(s.root, "sent", e.ID+".json"), e)
}

// Sent lists envelopes (without bodies) sent from one block, oldest first.
func (s *Store) Sent(fromBlock string) ([]envelope.Envelope, error) {
	files, err := filepath.Glob(filepath.Join(s.root, "sent", "pm-*.json"))
	if err != nil {
		return nil, err
	}
	var out []envelope.Envelope
	for _, f := range files {
		var e envelope.Envelope
		if err := readJSON(f, &e); err != nil {
			continue
		}
		if e.From.Block == fromBlock {
			out = append(out, e)
		}
	}
	return out, nil
}

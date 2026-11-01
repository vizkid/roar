package store

import (
	"slices"
	"testing"
)

// Every stored message and every state change leaves notify/<id>, and its
// revision grows. roard watches that one directory instead of every message.
func TestChangesNotifyAndRevisionGrows(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "hi")
	put(t, s, e)
	ids, err := s.TakeNotified()
	if err != nil || !slices.Equal(ids, []string{e.ID}) {
		t.Fatalf("after put: %v %v", ids, err)
	}
	if again, _ := s.TakeNotified(); len(again) != 0 {
		t.Fatalf("taken twice: %v", again)
	}
	m, _ := s.Get(e.ID)
	if m.Record.Revision != 1 {
		t.Fatalf("first revision %d", m.Record.Revision)
	}
	c, err := s.Claim(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Move(Queued, t0, func(r *Record) { r.HoldReason = "busy" }); err != nil {
		t.Fatal(err)
	}
	if err := c.Move(Submitting, t0, nil); err != nil {
		t.Fatal(err)
	}
	c.Release()
	if ids, _ := s.TakeNotified(); !slices.Equal(ids, []string{e.ID}) {
		t.Fatalf("after moves: %v", ids)
	}
	if m, _ := s.Get(e.ID); m.Record.Revision != 3 {
		t.Fatalf("revision after two moves %d", m.Record.Revision)
	}
}

// A duplicate put changes nothing, so it does not notify.
func TestDuplicateDoesNotNotify(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "hi")
	put(t, s, e)
	s.TakeNotified()
	if dup, err := s.Put(e, t0); err != nil || !dup {
		t.Fatal(dup, err)
	}
	if ids, _ := s.TakeNotified(); len(ids) != 0 {
		t.Fatalf("duplicate notified %v", ids)
	}
}

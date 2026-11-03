package store

import "testing"

// Review, 2026-11-03: Claude's 250 ms loop read every retained message's
// envelope and state, so a pane holding 1,000 finished messages cost about
// 8,000 file reads a second. Unfinished reads each state first and loads an
// envelope only for a message still in flight.
func TestUnfinishedLoadsOnlyLiveEnvelopes(t *testing.T) {
	s := newStore(t)
	var live string
	for i := range 20 {
		e := msg("block:r", "m")
		put(t, s, e)
		if i == 7 {
			live = e.ID
			continue
		}
		c, err := s.Claim(e.ID)
		if err != nil {
			t.Fatal(err)
		}
		c.Move(Read, t0, nil)
		c.Release()
	}
	before := envelopeReads.Load()
	msgs, err := s.Unfinished("block:r")
	if err != nil || len(msgs) != 1 || msgs[0].Envelope.ID != live {
		t.Fatalf("unfinished %d %v", len(msgs), err)
	}
	if n := envelopeReads.Load() - before; n != 1 {
		t.Fatalf("read %d envelopes for one live message", n)
	}
	if all, _ := s.Inbox("block:r"); len(all) != 20 {
		t.Fatalf("inbox %d", len(all))
	}
}

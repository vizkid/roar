package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/envelope"
)

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func msg(to string, text string) envelope.Envelope {
	return envelope.Envelope{
		ID:        envelope.NewID(t0),
		From:      envelope.Address{HostID: "h", Session: "session:s", Block: "block:sender"},
		To:        envelope.Address{HostID: "h", Session: "session:r", Block: to},
		Kind:      envelope.Inform,
		Text:      text,
		CreatedAt: t0,
	}
}

func put(t *testing.T, s *Store, e envelope.Envelope) {
	t.Helper()
	if dup, err := s.Put(e, t0); err != nil || dup {
		t.Fatalf("Put: dup=%v err=%v", dup, err)
	}
}

func TestPutGetAndDuplicate(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "hi")
	put(t, s, e)
	m, err := s.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Envelope.Text != "hi" || m.Record.State != Queued || !m.Record.UpdatedAt.Equal(t0) {
		t.Fatalf("got %+v", m)
	}
	dup, err := s.Put(e, t0)
	if err != nil || !dup {
		t.Fatalf("second Put: dup=%v err=%v", dup, err)
	}
	if _, err := s.Get(envelope.NewID(t0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: %v", err)
	}
}

func TestPutRejectsInvalidEnvelope(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	e.ID = "../escape"
	if _, err := s.Put(e, t0); err == nil {
		t.Fatal("invalid envelope stored")
	}
}

func TestLimits(t *testing.T) {
	s := newStore(t)
	s.Limits = Limits{PerRecipient: 10, PerHost: 15}
	put(t, s, msg("block:a", "12345678"))
	if _, err := s.Put(msg("block:a", "123"), t0); !errors.Is(err, ErrLimit) {
		t.Fatalf("per-recipient limit: %v", err)
	}
	put(t, s, msg("block:b", "123456"))
	if _, err := s.Put(msg("block:c", "12"), t0); !errors.Is(err, ErrLimit) {
		t.Fatalf("per-host limit: %v", err)
	}
}

func TestInboxOrderAndFilter(t *testing.T) {
	s := newStore(t)
	a := msg("block:r", "first")
	a.ID = envelope.NewID(t0)
	b := msg("block:r", "second")
	b.ID = envelope.NewID(t0.Add(time.Second))
	put(t, s, b)
	put(t, s, a)
	put(t, s, msg("block:other", "not mine"))
	in, err := s.Inbox("block:r")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 2 || in[0].Envelope.Text != "first" || in[1].Envelope.Text != "second" {
		t.Fatalf("inbox %+v", in)
	}
}

func TestTransitions(t *testing.T) {
	allowed := map[State][]State{
		Queued:     {Written, Submitting, Read, Expired},
		Written:    {Accepted, Read, Queued, Unknown, Expired},
		Accepted:   {Read, Queued, Expired},
		Submitting: {Submitted, Unknown},
		Unknown:    {Read, Queued},
	}
	all := []State{Queued, Written, Accepted, Read, Submitting, Submitted, Unknown, Expired}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, ok := range allowed[from] {
				want = want || ok == to
			}
			if got := CanMove(from, to); got != want {
				t.Errorf("CanMove(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestClaimMoveAndExclusive(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	put(t, s, e)
	c, err := s.Claim(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(e.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("second claim: %v", err)
	}
	if err := c.Move(Submitted, t0, nil); err == nil {
		t.Fatal("queued to submitted allowed")
	}
	if err := c.Move(Written, t0.Add(time.Second), func(r *Record) { r.Writer = "w1" }); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(e.ID)
	if m.Record.State != Written || m.Record.Writer != "w1" || !m.Record.UpdatedAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("record %+v", m.Record)
	}
	if _, err := s.Claim(e.ID); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
}

func TestWithdrawOnlyUnreadFromSender(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	put(t, s, e)
	if err := s.Withdraw(e.ID, "block:someone-else", t0); err == nil {
		t.Fatal("withdrawn by a stranger")
	}
	if err := s.Withdraw(e.ID, "block:sender", t0); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(e.ID)
	if m.Record.State != Expired || m.Record.Reason != "withdrawn" {
		t.Fatalf("record %+v", m.Record)
	}
}

func TestPruneKeepsUnread(t *testing.T) {
	s := newStore(t)
	unread := msg("block:r", "keep")
	done := msg("block:r", "drop")
	fresh := msg("block:r", "recent")
	for _, e := range []envelope.Envelope{unread, done, fresh} {
		put(t, s, e)
	}
	for id, at := range map[string]time.Time{done.ID: t0, fresh.ID: t0.Add(6 * 24 * time.Hour)} {
		c, err := s.Claim(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Move(Read, at, nil); err != nil {
			t.Fatal(err)
		}
		c.Release()
	}
	if err := s.Prune(t0.Add(8 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{unread.ID: true, done.ID: false, fresh.ID: true} {
		_, err := s.Get(id)
		if (err == nil) != want {
			t.Errorf("after prune, %s present=%v want %v", id, err == nil, want)
		}
	}
}

func TestSentRecords(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	if err := s.RecordSent(e); err != nil {
		t.Fatal(err)
	}
	sent, err := s.Sent("block:sender")
	if err != nil || len(sent) != 1 || sent[0].ID != e.ID || sent[0].To.Block != "block:r" {
		t.Fatalf("sent %+v err %v", sent, err)
	}
	if strings.Contains(sent[0].Text, "x") {
		t.Fatal("sent record keeps the body; it should keep only routing")
	}
}

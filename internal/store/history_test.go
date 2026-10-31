package store

import (
	"testing"
	"time"
)

// A message held for hours must show why, after its hold reason changed.
// Re-holding for the same reason on every sweep adds nothing.
func TestHistoryKeepsEachReason(t *testing.T) {
	s := newStore(t)
	e := msg("block:p", "x")
	put(t, s, e)
	steps := []struct {
		state  State
		reason string
	}{{Queued, "busy"}, {Queued, "busy"}, {Queued, "no_marker"}, {Submitting, ""}, {Submitted, ""}}
	for i, st := range steps {
		c, err := s.Claim(e.ID)
		if err != nil {
			t.Fatal(err)
		}
		at := t0.Add(time.Duration(i+1) * time.Minute)
		if err := c.Move(st.state, at, func(r *Record) { r.HoldReason = st.reason }); err != nil {
			t.Fatal(err)
		}
		c.Release()
	}
	m, _ := s.Get(e.ID)
	var got []string
	for _, h := range m.Record.History {
		got = append(got, string(h.State)+":"+h.Reason)
	}
	want := []string{"queued:", "queued:busy", "queued:no_marker", "submitting:", "submitted:"}
	if len(got) != len(want) {
		t.Fatalf("history %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history %q, want %q", got, want)
		}
	}
}

func TestHistoryIsBounded(t *testing.T) {
	var r Record
	for i := range MaxHistory + 5 {
		r.State, r.HoldReason = Queued, string(rune('a'+i%26))+string(rune('a'+i/26))
		r.note()
	}
	if len(r.History) != MaxHistory || r.History[len(r.History)-1].Reason != r.HoldReason {
		t.Fatalf("kept %d steps, last %+v", len(r.History), r.History[len(r.History)-1])
	}
}

package store

import "testing"

func TestCodexBindingRoundTrip(t *testing.T) {
	s := newStore(t)
	if _, ok := s.CodexBinding("block:p"); ok {
		t.Fatal("binding before any write")
	}
	want := CodexBinding{Thread: "T1", CodexHome: "/h/.codex", PID: 42, At: t0}
	if err := s.BindCodex("block:p", want); err != nil {
		t.Fatal(err)
	}
	want.Thread = "T2" // /new rebinds the same pane
	if err := s.BindCodex("block:p", want); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.CodexBinding("block:p"); !ok || got != want {
		t.Fatalf("got %+v, %v", got, ok)
	}
}

// A Codex sender holds the claim for its whole call, so a written message
// with a free claim lost its writer. Claude's adapter keeps messages written
// while it waits for receipt, so those stay.
func TestCrashedCodexWriterSettlesToUnknown(t *testing.T) {
	s := newStore(t)
	codex, claude := msg("block:r", "x"), msg("block:c", "y")
	put(t, s, codex)
	put(t, s, claude)
	for _, w := range []struct{ id, writer string }{{codex.ID, "codex:1:2"}, {claude.ID, "mcp:1:2"}} {
		c, err := s.Claim(w.id)
		if err != nil {
			t.Fatal(err)
		}
		c.Move(Written, t0, func(r *Record) { r.Writer = w.writer })
		c.Release()
	}
	if n, err := s.SettleOrphans(t0); err != nil || n != 1 {
		t.Fatalf("settled %d, err %v", n, err)
	}
	if m, _ := s.Get(codex.ID); m.Record.State != Unknown || m.Record.Reason != "interrupted_while_writing" {
		t.Fatalf("codex record %+v", m.Record)
	}
	if m, _ := s.Get(claude.ID); m.Record.State != Written {
		t.Fatalf("claude record %+v", m.Record)
	}
}

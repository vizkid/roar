package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The binding holds a token that can drive the agy session, so only this
// user may read it, and unbinding removes it.
func TestBindingKeepsTokenPrivate(t *testing.T) {
	s := newStore(t)
	if _, ok := s.AgyBinding("block:p"); ok {
		t.Fatal("binding before any write")
	}
	want := AgyBinding{Conversation: "C1", Address: "localhost:1", Token: "T", Exe: "/u/agy", Session: "session:s", PID: 42, At: t0}
	if err := s.BindAgy("block:p", want); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.AgyBinding("block:p"); !ok || got != want {
		t.Fatalf("got %+v, %v", got, ok)
	}
	fi, err := os.Stat(filepath.Join(s.root, "agy", "block:p.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("binding file: %v %v", fi, err)
	}
	if dir, err := os.Stat(filepath.Join(s.root, "agy")); err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("binding dir: %v %v", dir, err)
	}
	if err := s.BindAgy("../x", want); err == nil {
		t.Fatal("bound a path outside the store")
	}
	s.UnbindAgy("block:p")
	if _, ok := s.AgyBinding("block:p"); ok {
		t.Fatal("binding survived unbind")
	}
}

// An agy sender holds the claim for its whole call, as a Codex one does.
func TestCrashedAgyWriterSettlesToUnknown(t *testing.T) {
	s := newStore(t)
	m := msg("block:a", "x")
	put(t, s, m)
	c, err := s.Claim(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Move(Written, t0, func(r *Record) { r.Writer = "agy:1:2" })
	c.Release()
	if n, err := s.SettleOrphans(t0); err != nil || n != 1 {
		t.Fatalf("settled %d, err %v", n, err)
	}
	if got, _ := s.Get(m.ID); got.Record.State != Unknown || got.Record.Reason != "interrupted_while_writing" {
		t.Fatalf("record %+v", got.Record)
	}
}

// A binding whose agy exited holds a dead token. Pruning deletes it, even
// when no message is sent to that pane again; a live one stays.
func TestPruneAgyDropsExited(t *testing.T) {
	s := newStore(t)
	done := exec.Command("true")
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	s.BindAgy("block:gone", AgyBinding{Conversation: "C1", Token: "T", PID: done.Process.Pid})
	s.BindAgy("block:live", AgyBinding{Conversation: "C2", Token: "T", PID: os.Getpid()})
	s.PruneAgy()
	if _, ok := s.AgyBinding("block:gone"); ok {
		t.Fatal("kept the binding of an exited agy")
	}
	if _, ok := s.AgyBinding("block:live"); !ok {
		t.Fatal("deleted a live binding")
	}
}

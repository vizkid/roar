package codex

import (
	"context"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/store"
)

const block = "block:codex-pane"

func put(t *testing.T, st *store.Store) string {
	t.Helper()
	e := envelope.Envelope{
		ID: envelope.NewID(time.Now()), Kind: envelope.Inform, Text: "hi", CreatedAt: time.Now(),
		From: envelope.Address{HostID: "h", Session: "session:a", Block: "block:a", Label: "a"},
		To:   envelope.Address{HostID: "h", Session: "session:s", Block: block},
	}
	if _, err := st.Put(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func waitState(t *testing.T, st *store.Store, id string, want store.State) store.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := st.Get(id); m.Record.State == want {
			return m.Record
		}
		time.Sleep(20 * time.Millisecond)
	}
	m, _ := st.Get(id)
	t.Fatalf("%s is %+v, want %s", id, m.Record, want)
	return store.Record{}
}

func runAdapter(t *testing.T, st *store.Store, p *Proxy, writer string) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Adapter{Store: st, Block: block, Proxy: p, Writer: writer, Now: time.Now, Poll: 50 * time.Millisecond}
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestAdapterDeliversAndRecordsReceipt(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	st, _ := store.Open(t.TempDir())
	id := put(t, st)
	runAdapter(t, st, p, "w1")
	time.Sleep(200 * time.Millisecond)
	if m, _ := st.Get(id); m.Record.State != store.Queued || m.Record.HoldReason != "codex_not_ready" {
		t.Fatalf("before the TUI: %+v", m.Record)
	}
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	rec := waitState(t, st, id, store.Read)
	if rec.Proof != "codex_turn_start" || rec.Boundary != "turn" {
		t.Fatalf("receipt %+v", rec)
	}
	id2 := put(t, st)
	rec = waitState(t, st, id2, store.Read)
	if rec.Proof != "codex_turn_steer" || rec.Boundary != "mid_turn" {
		t.Fatalf("second message should steer the running turn: %+v", rec)
	}
}

func TestAdapterLeavesUnknownAfterDisconnect(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	st, _ := store.Open(t.TempDir())
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	s.mu.Lock()
	s.holdFor = "turn/start"
	s.mu.Unlock()
	id := put(t, st)
	runAdapter(t, st, p, "w1")
	waitState(t, st, id, store.Written)
	s.dropAll()
	if rec := waitState(t, st, id, store.Unknown); rec.Reason == "" {
		t.Fatal("unknown without a reason")
	}
}

func TestAdapterSettlesAPreviousWritersMessageAsUnknown(t *testing.T) {
	s := newFakeServer(t)
	p, _ := startProxy(t, s)
	st, _ := store.Open(t.TempDir())
	id := put(t, st)
	c, _ := st.Claim(id)
	c.Move(store.Written, time.Now(), func(r *store.Record) { r.Writer = "old" })
	c.Release()
	runAdapter(t, st, p, "new")
	waitState(t, st, id, store.Unknown)
}

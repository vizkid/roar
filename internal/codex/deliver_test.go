package codex

import (
	"context"
	"slices"
	"strings"
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

// deliver claims one message and runs Deliver on it, as a sender would.
func deliver(t *testing.T, st *store.Store, d *fakeDaemon, id string) (bool, store.Record) {
	t.Helper()
	c, err := st.Claim(id)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	b := store.CodexBinding{Thread: "T1", CodexHome: d.home}
	handled, err := Deliver(context.Background(), c, b, "codex:test", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return handled, c.Message().Record
}

func open(t *testing.T) *store.Store {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestIdleThreadStartsTurn(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "idle", "")
	st := open(t)
	id := put(t, st)
	handled, rec := deliver(t, st, d, id)
	if !handled || rec.State != store.Read || rec.Proof != "codex_turn_start" || rec.Boundary != "turn" {
		t.Fatalf("handled %v, record %+v", handled, rec)
	}
	if rec.Writer != "codex:test" {
		t.Fatalf("writer %q", rec.Writer)
	}
	if len(d.inputs) != 1 || !strings.Contains(d.inputs[0], "[roar "+id) || !strings.Contains(d.inputs[0], "hi") {
		t.Fatalf("input %q", d.inputs)
	}
}

func TestBusyThreadIsSteered(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "active", "U9")
	st := open(t)
	handled, rec := deliver(t, st, d, put(t, st))
	if !handled || rec.State != store.Read || rec.Proof != "codex_turn_steer" || rec.Boundary != "mid_turn" {
		t.Fatalf("handled %v, record %+v", handled, rec)
	}
	if !slices.Contains(d.called(), "thread/turns/list") {
		t.Fatalf("steered without reading the turn: %v", d.called())
	}
}

// A turn can end between the status read and the steer. The rejected steer
// sent nothing, so Roar reads again and starts a turn.
func TestStaleSteerIsSentAgain(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "active", "U9")
	d.endTurn = true
	st := open(t)
	handled, rec := deliver(t, st, d, put(t, st))
	if !handled || rec.State != store.Read || rec.Proof != "codex_turn_start" {
		t.Fatalf("handled %v, record %+v; calls %v", handled, rec, d.called())
	}
	if len(d.inputs) != 1 {
		t.Fatalf("delivered %d times", len(d.inputs))
	}
}

func TestRejectedCallHolds(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "idle", "")
	d.reject = "turn/start"
	st := open(t)
	handled, rec := deliver(t, st, d, put(t, st))
	if !handled || rec.State != store.Queued || rec.HoldReason != "codex_rejected" || !strings.Contains(rec.Reason, "refused by test") {
		t.Fatalf("handled %v, record %+v", handled, rec)
	}
}

func TestDisconnectAfterWriteIsUnknown(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "idle", "")
	d.holdFor = "turn/start"
	st := open(t)
	id := put(t, st)
	go func() {
		for {
			if m, _ := st.Get(id); m.Record.State == store.Written {
				d.dropAll()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	handled, rec := deliver(t, st, d, id)
	if !handled || rec.State != store.Unknown || rec.Reason == "" {
		t.Fatalf("handled %v, record %+v", handled, rec)
	}
}

// Nothing was sent, so the caller may fall back: the message stays queued.
func TestUnloadedThreadFallsBack(t *testing.T) {
	for _, status := range []string{"notLoaded", "systemError", "missing"} {
		d := newFakeDaemon(t)
		if status != "missing" {
			d.set("T1", status, "")
		}
		st := open(t)
		handled, rec := deliver(t, st, d, put(t, st))
		if handled || rec.State != store.Queued {
			t.Fatalf("%s: handled %v, record %+v", status, handled, rec)
		}
		if slices.Contains(d.called(), "turn/start") || slices.Contains(d.called(), "turn/steer") {
			t.Fatalf("%s: sent a turn: %v", status, d.called())
		}
	}
}

func TestMissingSocketFallsBack(t *testing.T) {
	st := open(t)
	id := put(t, st)
	c, _ := st.Claim(id)
	defer c.Release()
	b := store.CodexBinding{Thread: "T1", CodexHome: t.TempDir()}
	handled, err := Deliver(context.Background(), c, b, "codex:test", time.Now)
	if err != nil || handled || c.Message().Record.State != store.Queued {
		t.Fatalf("handled %v, err %v, record %+v", handled, err, c.Message().Record)
	}
}

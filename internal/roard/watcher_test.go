package roard

import (
	"context"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/store"
)

// 45: anonymous callers and local callers with another uid are refused.
func TestCallerCheck(t *testing.T) {
	uid, other := 501, 502
	for _, c := range []struct {
		p    rexctl.Principal
		want bool
	}{
		{rexctl.Principal{Kind: "anonymous"}, false},
		{rexctl.Principal{Kind: "peer", UID: &other}, false},
		{rexctl.Principal{Kind: "peer"}, false},
		{rexctl.Principal{Kind: "peer", UID: &uid}, true},
		{rexctl.Principal{Kind: "tailscale", UserID: "userid:1"}, true},
		{rexctl.Principal{Kind: "windows_peer"}, false},
	} {
		if got := allowed(c.p, uid); got != c.want {
			t.Fatalf("%+v: %v", c.p, got)
		}
	}
}

func hold(t *testing.T, st *store.Store, id, reason string) {
	t.Helper()
	c, err := st.Claim(id)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	if err := c.Move(store.Queued, time.Now(), func(r *store.Record) { r.HoldReason = reason }); err != nil {
		t.Fatal(err)
	}
}

// 54: no timer runs without pending work, nor for work an event covers.
// Only a hold that no event reports arms the re-check.
func TestGapTimerOnlyWhenNeeded(t *testing.T) {
	h := newHarness(t, "")
	w := newWatcher(context.Background(), h.deps, newSubs(nil), newPool(1))
	defer w.stop()
	if w.gapArmed() {
		t.Fatal("armed with no work")
	}
	e := h.envelope("busy")
	h.st.Put(e, time.Now())
	hold(t, h.st, e.ID, "busy")
	w.refresh(e.To.Block)
	if w.gapArmed() {
		t.Fatal("armed for a busy hold, which program status events cover")
	}
	hold(t, h.st, e.ID, "draft")
	w.refresh(e.To.Block)
	if !w.gapArmed() {
		t.Fatal("not armed for a draft hold, which no event reports")
	}
	c, _ := h.st.Claim(e.ID)
	c.Move(store.Read, time.Now(), nil)
	c.Release()
	w.refresh(e.To.Block)
	if w.gapArmed() {
		t.Fatal("still armed after the message was read")
	}
}

// Terminal events for a pane with pending work schedule an attempt; others
// cost nothing.
func TestRexEventRouting(t *testing.T) {
	h := newHarness(t, "watcher.coalesce=10ms")
	w := newWatcher(context.Background(), h.deps, newSubs(nil), newPool(1))
	defer w.stop()
	w.haveLock(true)
	e := h.envelope("wake")
	h.st.Put(e, time.Now())
	hold(t, h.st, e.ID, "busy")
	w.refresh(e.To.Block)
	before := h.del.count("block:r")
	w.rexEvent(rexctl.Event{Type: "block_event", Payload: []byte(`{"block_id":"block:other","name":"program_status_changed"}`)})
	w.rexEvent(rexctl.Event{Type: "block_event", Payload: []byte(`{"block_id":"block:r","name":"bell"}`)})
	time.Sleep(50 * time.Millisecond)
	if h.del.count("block:r") != before {
		t.Fatal("an unrelated event caused an attempt")
	}
	for range 3 { // coalesced into one
		w.rexEvent(rexctl.Event{Type: "block_event", Payload: []byte(`{"block_id":"block:r","name":"program_status_changed"}`)})
	}
	time.Sleep(100 * time.Millisecond)
	if got := h.del.count("block:r") - before; got != 1 {
		t.Fatalf("attempts after three events: %d", got)
	}
}

// Live, 2026-11-02: held messages sat through a pane's rests and its close
// until a restart. The sweep re-reads the store, so even a wrong pending
// flag cannot strand a message.
func TestSweepRecoversMissedWake(t *testing.T) {
	h := newHarness(t, "watcher.sweep=50ms,watcher.coalesce=1ms")
	p := newPool(1)
	defer p.stop()
	w := newWatcher(context.Background(), h.deps, newSubs(nil), p)
	defer w.stop()
	w.haveLock(true)
	e := h.envelope("held")
	h.st.Put(e, time.Now())
	hold(t, h.st, e.ID, "busy")
	w.refresh(e.To.Block)
	w.mu.Lock()
	delete(w.pending, e.To.Block) // the state the live pane was in
	w.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for h.del.count(e.To.Block) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the sweep never attempted the pane")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The sweep is a timer only while something is queued.
func TestSweepOnlyWhileQueued(t *testing.T) {
	h := newHarness(t, "watcher.sweep=20ms")
	p := newPool(1)
	defer p.stop()
	w := newWatcher(context.Background(), h.deps, newSubs(nil), p)
	defer w.stop()
	if w.sweepArmed() {
		t.Fatal("armed with nothing queued")
	}
	e := h.envelope("held")
	h.st.Put(e, time.Now())
	hold(t, h.st, e.ID, "busy")
	w.refresh(e.To.Block)
	if !w.sweepArmed() {
		t.Fatal("not armed with a message queued")
	}
	c, _ := h.st.Claim(e.ID)
	c.Move(store.Read, time.Now(), nil)
	c.Release()
	deadline := time.Now().Add(5 * time.Second)
	for w.sweepArmed() {
		if time.Now().After(deadline) {
			t.Fatal("still armed with nothing queued")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

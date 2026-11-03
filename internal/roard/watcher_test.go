package roard

import (
	"context"
	"testing"
	"time"

	"github.com/vizkid/rexkit/rexctl"
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
	w := newWatcher(context.Background(), h.deps, newSubs(nil), newPool(1, 64))
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

// sessionEvent is a session's event as the server sends it: wrapped in a
// session_event. The first tests fed the inner event bare, so roard passed
// them and ignored every live event (2026-11-03).
func sessionEvent(typ, payload string) rexctl.Event {
	return rexctl.Event{Type: "session_event", Payload: []byte(`{"session_id":"session:s","event":{"type":"` + typ + `","payload":` + payload + `}}`)}
}

func blockEvent(block, name string) rexctl.Event {
	return sessionEvent("block_event", `{"block_id":"`+block+`","creator_name":"com.superlogical.terminal","name":"`+name+`"}`)
}

// Live, 2026-11-03: an attempt read the agent as working, its done event
// came during that attempt, and the attempt the event caused lost the
// message lock to it. Nothing else came, so the message waited for the
// sweep. An event during an attempt runs one more attempt after it.
func TestEventDuringAttemptRunsAgain(t *testing.T) {
	h := newHarness(t, "watcher.coalesce=1ms")
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	h.del.fn = func(string) {
		started <- struct{}{}
		if h.del.count("block:r") == 1 {
			<-release // the first attempt is still reading the agent
		}
	}
	w := newWatcher(context.Background(), h.deps, newSubs(nil), newPool(2, 64))
	defer w.stop()
	w.haveLock(true)
	e := h.envelope("raced")
	h.st.Put(e, time.Now())
	hold(t, h.st, e.ID, "busy")
	w.cover(e.To.Block)
	<-started
	w.rexEvent(blockEvent("block:r", "program_status_changed"))
	time.Sleep(50 * time.Millisecond) // the event's own attempt runs and ends
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for h.del.count("block:r") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("attempts: %d; none after the attempt the event overlapped", h.del.count("block:r"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The relay check reads the same events as the watcher. It checked the
// wrapped event for block_closed and so never ran on a pane close.
func TestRelayChecksOnWrappedClose(t *testing.T) {
	for _, c := range []struct {
		e    rexctl.Event
		want bool
	}{
		{sessionEvent("block_closed", `{"block_id":"block:r"}`), true},
		{rexctl.Event{Type: "session_destroyed", Payload: []byte(`{"session_id":"session:s"}`)}, true},
		{blockEvent("block:r", "program_status_changed"), false},
		{rexctl.Event{Type: "client_left", Payload: []byte(`{"client_id":"client:c"}`)}, false},
	} {
		if got := relayTrigger(unwrap(c.e)); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.e.Type, c.e.Payload, got, c.want)
		}
	}
}

// A pane that closes with a held message gets an attempt, which expires it.
func TestBlockClosedSchedulesAttempt(t *testing.T) {
	h := newHarness(t, "watcher.coalesce=10ms")
	w := newWatcher(context.Background(), h.deps, newSubs(nil), newPool(1, 64))
	defer w.stop()
	w.haveLock(true)
	e := h.envelope("closing")
	h.st.Put(e, time.Now())
	hold(t, h.st, e.ID, "busy")
	w.refresh(e.To.Block)
	before := h.del.count("block:r")
	w.rexEvent(sessionEvent("block_closed", `{"block_id":"block:r"}`))
	time.Sleep(100 * time.Millisecond)
	if got := h.del.count("block:r") - before; got != 1 {
		t.Fatalf("attempts after block_closed: %d", got)
	}
}

// Terminal events for a pane with pending work schedule an attempt; others
// cost nothing.
func TestRexEventRouting(t *testing.T) {
	h := newHarness(t, "watcher.coalesce=10ms")
	w := newWatcher(context.Background(), h.deps, newSubs(nil), newPool(1, 64))
	defer w.stop()
	w.haveLock(true)
	e := h.envelope("wake")
	h.st.Put(e, time.Now())
	hold(t, h.st, e.ID, "busy")
	w.refresh(e.To.Block)
	before := h.del.count("block:r")
	w.rexEvent(blockEvent("block:other", "program_status_changed"))
	w.rexEvent(blockEvent("block:r", "bell"))
	time.Sleep(50 * time.Millisecond)
	if h.del.count("block:r") != before {
		t.Fatal("an unrelated event caused an attempt")
	}
	for range 3 { // coalesced into one
		w.rexEvent(blockEvent("block:r", "program_status_changed"))
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
	p := newPool(1, 64)
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
	p := newPool(1, 64)
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

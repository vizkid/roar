package fallback

import (
	"context"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// A queued message needs the watcher unless a native adapter serves its
// pane. Messages in any other state never do.
func TestNeedsWatcher(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, st: st, d: &Deliverer{Store: st, Now: time.Now}}
	needs := func() bool {
		t.Helper()
		ok, err := f.d.NeedsWatcher()
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if needs() {
		t.Fatal("empty store needs a watcher")
	}
	e := f.queue("session:s", "block:b", "held")
	if !needs() {
		t.Fatal("queued message without an adapter needs a watcher")
	}
	lock, err := st.HoldAdapter("block:b")
	if err != nil {
		t.Fatal(err)
	}
	if needs() {
		t.Fatal("a native adapter delivers this one; no watcher needed")
	}
	lock.Release()
	c, err := st.Claim(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Move(store.Expired, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	c.Release()
	if needs() {
		t.Fatal("only queued messages need the watcher")
	}
}

func watchReturns(t *testing.T, d *Deliverer, within time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), within+5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Watch(ctx, true) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(within):
		t.Fatalf("watch --until-idle still running after %s", within)
	}
}

func TestWatchUntilIdleExitsWhenNothingHeld(t *testing.T) {
	f := setup(t)
	watchReturns(t, f.d, 3*time.Second)
	if !f.st.WatcherFree() {
		t.Fatal("the watcher kept watch.lock after exiting")
	}
}

// A second watcher leaves the work to the first and touches nothing.
func TestWatchUntilIdleYieldsToRunningWatcher(t *testing.T) {
	f := setup(t)
	lock, err := f.st.HoldWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	e := f.queue("session:s", "block:b", "held")
	watchReturns(t, f.d, 3*time.Second)
	if m, _ := f.st.Get(e.ID); m.Record.State != store.Queued || m.Record.HoldReason != "" {
		t.Fatalf("second watcher touched the message: %+v", m.Record)
	}
}

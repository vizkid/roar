package cli

import (
	"context"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/store"
)

// The fallback stands aside for a native adapter with hold reason
// native_adapter. send must wait through that to the adapter's outcome.
func TestSettleWaitsForTheAdapter(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	now := time.Now()
	e := envelope.Envelope{
		ID: envelope.NewID(now), Kind: envelope.Inform, Text: "hi", CreatedAt: now,
		From: envelope.Address{HostID: "h", Session: "session:a", Block: "block:a"},
		To:   envelope.Address{HostID: "h", Session: "session:b", Block: "block:b"},
	}
	if _, err := st.Put(e, now); err != nil {
		t.Fatal(err)
	}
	move := func(to store.State, edit func(*store.Record)) {
		c, err := st.Claim(e.ID)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Release()
		c.Move(to, time.Now(), edit)
	}
	move(store.Queued, func(r *store.Record) { r.HoldReason = "native_adapter" })
	go func() {
		time.Sleep(300 * time.Millisecond)
		move(store.Written, nil)
		move(store.Read, nil)
	}()
	m, err := settle(context.Background(), st, e.ID)
	if err != nil || m.Record.State != store.Read {
		t.Fatalf("settled at %+v, %v", m.Record, err)
	}
}

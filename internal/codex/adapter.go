package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// DeliverTimeout bounds one injected turn call.
const DeliverTimeout = 15 * time.Second

// Adapter delivers one pane's queued messages through the proxy. The caller
// holds the pane's adapter lock for as long as Run runs.
type Adapter struct {
	Store  *store.Store
	Block  string
	Proxy  *Proxy
	Writer string
	Now    func() time.Time
	Poll   time.Duration
	Log    io.Writer
}

// Run delivers until ctx ends.
func (a *Adapter) Run(ctx context.Context) {
	if a.Poll == 0 {
		a.Poll = 250 * time.Millisecond
	}
	if a.Log == nil {
		a.Log = io.Discard
	}
	a.recover()
	t := time.NewTicker(a.Poll)
	defer t.Stop()
	for {
		a.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// recover settles messages a previous adapter wrote without a result. A
// Codex turn can run even after its caller disconnected, so they are
// unknown, never replayed.
func (a *Adapter) recover() {
	msgs, _ := a.Store.Inbox(a.Block)
	for _, m := range msgs {
		if m.Record.State == store.Written && m.Record.Writer != a.Writer {
			a.move(m.Envelope.ID, store.Unknown, func(r *store.Record) { r.Reason = "adapter_restarted" })
		}
	}
}

func (a *Adapter) pass(ctx context.Context) {
	msgs, err := a.Store.Inbox(a.Block)
	if err != nil {
		return
	}
	for _, m := range msgs {
		if m.Record.State == store.Queued && !a.deliver(ctx, m.Envelope.ID) {
			return // later messages would meet the same obstacle
		}
	}
}

// deliver sends one message. It returns false when delivery should pause.
func (a *Adapter) deliver(ctx context.Context, id string) bool {
	c, err := a.Store.Claim(id)
	if err != nil {
		return true
	}
	defer c.Release()
	m := c.Message()
	if m.Record.State != store.Queued {
		return true
	}
	// Record the write first: a crash after it must not look queued.
	now := a.Now()
	if err := c.Move(store.Written, now, func(r *store.Record) { r.Writer, r.WrittenAt = a.Writer, now }); err != nil {
		return false
	}
	dctx, cancel := context.WithTimeout(ctx, DeliverTimeout)
	r, err := a.Proxy.Deliver(dctx, m.Render())
	cancel()
	switch {
	case err == nil:
		proof, boundary := "codex_turn_start", "turn"
		if r.Method == "turn/steer" {
			proof, boundary = "codex_turn_steer", "mid_turn"
		}
		c.Move(store.Read, a.Now(), func(rec *store.Record) { rec.Proof, rec.Boundary = proof, boundary })
		return true
	case errors.Is(err, ErrNotReady):
		c.Move(store.Queued, a.Now(), func(rec *store.Record) { rec.HoldReason = "codex_not_ready" })
	case errors.Is(err, ErrRejected):
		fmt.Fprintf(a.Log, "roar codex: %s: %v\n", id, err)
		c.Move(store.Queued, a.Now(), func(rec *store.Record) { rec.HoldReason, rec.Reason = "codex_rejected", err.Error() })
	default:
		c.Move(store.Unknown, a.Now(), func(rec *store.Record) { rec.Reason = err.Error() })
		return true
	}
	return false
}

func (a *Adapter) move(id string, to store.State, edit func(*store.Record)) {
	c, err := a.Store.Claim(id)
	if err != nil {
		return
	}
	defer c.Release()
	c.Move(to, a.Now(), edit)
}

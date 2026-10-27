package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vizkid/ping/internal/envelope"
	"github.com/vizkid/ping/internal/store"
)

// deliverer writes queued messages as channel notifications and turns
// transcript records into receipts.
type deliverer struct {
	cfg     Config
	w       *writer
	path    string
	offset  int64
	partial []byte
	removed map[string]time.Time // remove records still inside the grace period
}

func newDeliverer(cfg Config, w *writer) *deliverer {
	return &deliverer{cfg: cfg, w: w, removed: map[string]time.Time{}}
}

func (d *deliverer) run(ctx context.Context) {
	d.scan(true)
	d.recover()
	t := time.NewTicker(d.cfg.Poll)
	defer t.Stop()
	for {
		d.scan(false)
		d.settleRemoved()
		d.deliver(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// recover settles messages an earlier adapter left in flight. Records from
// the transcript were applied by the first scan; whatever is still written
// or accepted by another writer is replayed, because a changed writer means
// Claude's queue for it is gone.
func (d *deliverer) recover() {
	msgs, _ := d.cfg.Store.Inbox(d.cfg.Block)
	for _, m := range msgs {
		s := m.Record.State
		if (s == store.Written || s == store.Accepted) && m.Record.Writer != d.cfg.Writer {
			d.move(m.Envelope.ID, store.Queued, func(r *store.Record) { r.Reason = "writer_changed" })
		}
	}
}

func (d *deliverer) deliver(ctx context.Context) {
	msgs, err := d.cfg.Store.Inbox(d.cfg.Block)
	if err != nil {
		return
	}
	for _, m := range msgs {
		if m.Record.State != store.Queued {
			continue
		}
		if m.Envelope.Kind == envelope.Request {
			// A request that lands mid-turn reaches the model as untrusted
			// information, so wait until Claude is at rest.
			st, err := d.cfg.AgentStatus(ctx)
			if err != nil || (st != "idle" && st != "done") {
				d.hold(m, "busy")
				continue
			}
		}
		d.write(m)
	}
}

func (d *deliverer) hold(m store.Message, reason string) {
	if m.Record.HoldReason == reason {
		return
	}
	d.move(m.Envelope.ID, store.Queued, func(r *store.Record) { r.HoldReason = reason })
}

func (d *deliverer) write(m store.Message) {
	c, err := d.cfg.Store.Claim(m.Envelope.ID)
	if err != nil {
		return
	}
	defer c.Release()
	if c.Message().Record.State != store.Queued {
		return
	}
	e := c.Message().Envelope
	line, err := notification(c.Message().Render(), map[string]string{"id": e.ID, "sender": envelope.Sanitize(e.From.Label)})
	if err != nil {
		fmt.Fprintf(d.cfg.Log, "ping mcp: %s: %v\n", e.ID, err)
		return
	}
	// Record the write first: a crash after the write must not look queued.
	now := d.cfg.Now()
	if err := c.Move(store.Written, now, func(r *store.Record) { r.Writer, r.WrittenAt = d.cfg.Writer, now }); err != nil {
		return
	}
	if err := d.w.line(line); err != nil {
		fmt.Fprintf(d.cfg.Log, "ping mcp: writing %s: %v\n", e.ID, err)
	}
}

// scan reads new transcript lines. A changed path (after /clear) starts the
// new file from the beginning.
func (d *deliverer) scan(fromStart bool) {
	path, err := d.cfg.Transcript()
	if err != nil {
		return
	}
	if path != d.path || fromStart {
		d.path, d.offset, d.partial = path, 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(d.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReader(f)
	for {
		chunk, err := r.ReadBytes('\n')
		d.offset += int64(len(chunk))
		if errors.Is(err, io.EOF) {
			d.partial = append(d.partial, chunk...)
			return
		}
		if err != nil {
			return
		}
		line := append(d.partial, chunk...)
		d.partial = nil
		if ev, ok := ParseEvent(line); ok {
			d.apply(ev)
		}
	}
}

func (d *deliverer) apply(ev Event) {
	m, err := d.cfg.Store.Get(ev.ID)
	if err != nil || m.Envelope.To.Block != d.cfg.Block {
		return
	}
	// Ignore records older than the write they would answer.
	if ev.At.Before(m.Record.WrittenAt.Truncate(time.Millisecond)) {
		return
	}
	switch ev.Kind {
	case KindAccepted:
		if m.Record.State == store.Written {
			d.move(ev.ID, store.Accepted, nil)
		}
	case KindRemoved:
		if m.Record.State == store.Written || m.Record.State == store.Accepted {
			d.removed[ev.ID] = d.cfg.Now()
		}
	case KindReceivedTurn, KindReceivedMidTurn:
		boundary := "turn"
		if ev.Kind == KindReceivedMidTurn {
			boundary = "mid_turn"
		}
		if m.Record.State == store.Written || m.Record.State == store.Accepted {
			d.move(ev.ID, store.Read, func(r *store.Record) { r.Proof, r.Boundary = "claude_transcript", boundary })
		}
		delete(d.removed, ev.ID)
	}
}

// settleRemoved requeues a removed message that got no receipt within the
// grace period. A receipt can follow its remove record by a moment.
func (d *deliverer) settleRemoved() {
	for id, at := range d.removed {
		if d.cfg.Now().Sub(at) < d.cfg.Grace {
			continue
		}
		delete(d.removed, id)
		d.scan(false)
		if m, err := d.cfg.Store.Get(id); err == nil && m.Record.State != store.Read {
			d.move(id, store.Queued, func(r *store.Record) { r.Reason = "removed_without_receipt" })
		}
	}
}

func (d *deliverer) move(id string, to store.State, edit func(*store.Record)) {
	c, err := d.cfg.Store.Claim(id)
	if err != nil {
		return
	}
	defer c.Release()
	if c.Message().Record.State == to && edit == nil {
		return
	}
	if err := c.Move(to, d.cfg.Now(), edit); err != nil {
		fmt.Fprintf(d.cfg.Log, "ping mcp: %v\n", err)
	}
}

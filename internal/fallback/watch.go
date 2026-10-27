package fallback

import (
	"context"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// Sweep is how often Watch re-checks every held message, as a backstop for
// events it missed.
const Sweep = 30 * time.Second

// Block attempts the oldest queued message for one pane. One per pass: a
// delivered message starts a turn, so the next would only be held as busy.
func (d *Deliverer) Block(ctx context.Context, block string) (*Outcome, error) {
	msgs, err := d.Store.Inbox(block)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if m.Record.State == store.Queued {
			out, err := d.Attempt(ctx, m.Envelope.ID)
			return &out, err
		}
	}
	return nil, nil
}

// All runs one pass over every pane with queued messages.
func (d *Deliverer) All(ctx context.Context) ([]Outcome, error) {
	if _, err := d.Store.SettleOrphans(d.Now()); err != nil {
		return nil, err
	}
	blocks, err := d.queuedBlocks()
	if err != nil {
		return nil, err
	}
	var outs []Outcome
	for b := range blocks {
		out, err := d.Block(ctx, b)
		if err != nil {
			return outs, err
		}
		if out != nil {
			outs = append(outs, *out)
		}
	}
	return outs, nil
}

func (d *Deliverer) queuedBlocks() (map[string]bool, error) {
	all, err := d.Store.All()
	if err != nil {
		return nil, err
	}
	blocks := map[string]bool{}
	for _, m := range all {
		if m.Record.State == store.Queued {
			blocks[m.Envelope.To.Block] = true
		}
	}
	return blocks, nil
}

// triggers are the terminal events after which a held message may be
// deliverable: the agent changed state, or the pane's program changed.
var triggers = map[string]bool{"program_status_changed": true, "process_changed": true, "child_exited": true}

// Watch delivers held messages as their panes come to rest. It follows Rex
// events and sweeps every Sweep. It returns when ctx ends.
func (d *Deliverer) Watch(ctx context.Context) error {
	d.All(ctx)
	sweep := time.NewTicker(Sweep)
	defer sweep.Stop()
	for ctx.Err() == nil {
		events, err := d.Rex.Events(ctx)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
	stream:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-sweep.C:
				d.All(ctx)
			case ev, ok := <-events:
				if !ok {
					break stream // the stream broke; reconnect
				}
				if !triggers[ev.Name] {
					continue
				}
				if blocks, err := d.queuedBlocks(); err == nil && blocks[ev.Block] {
					d.Block(ctx, ev.Block)
				}
			}
		}
		time.Sleep(time.Second)
	}
	return nil
}

package fallback

import (
	"context"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// Sweep is how often Watch re-checks every held message, as a backstop for
// events it missed.
const Sweep = 30 * time.Second

// CatchUp is how long after starting `rex events` the watcher sweeps once
// more. Rex sends nothing when the stream connects, so a change between the
// last sweep and the connection would otherwise wait for the next Sweep.
const CatchUp = 2 * time.Second

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
	d.Store.PruneAgy()
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
	return d.Store.QueuedBlocks()
}

// NeedsWatcher reports whether a queued message waits for a pane that no
// native adapter serves. Only the watcher would retry such a message.
func (d *Deliverer) NeedsWatcher() (bool, error) {
	blocks, err := d.queuedBlocks()
	if err != nil {
		return false, err
	}
	for b := range blocks {
		if !d.Store.AdapterLive(b) {
			return true, nil
		}
	}
	return false, nil
}

// triggers are the terminal events after which a held message may be
// deliverable: the agent changed state, or the pane's program changed.
var triggers = map[string]bool{"program_status_changed": true, "process_changed": true, "child_exited": true}

// Watch delivers held messages as their panes come to rest. It follows Rex
// events and sweeps every Sweep, holding watch.lock throughout. With
// untilIdle it exits at once if another watcher runs, and returns once no
// message needs a watcher; otherwise it waits for the lock and runs until
// ctx ends.
func (d *Deliverer) Watch(ctx context.Context, untilIdle bool) error {
	w := &watch{d: d, untilIdle: untilIdle}
	if !w.hold(ctx) {
		return nil
	}
	defer w.release()
	d.All(ctx)
	sweep := time.NewTicker(Sweep)
	defer sweep.Stop()
	for ctx.Err() == nil && !w.done() {
		events, err := d.Rex.Events(ctx)
		if err != nil {
			// Keep sweeping while the event stream is down.
			select {
			case <-ctx.Done():
			case <-sweep.C:
				d.All(ctx)
			case <-time.After(time.Second):
			}
			continue
		}
		catchUp := time.After(CatchUp)
	stream:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-catchUp:
				d.All(ctx)
			case <-sweep.C:
				d.All(ctx)
			case ev, ok := <-events:
				if !ok {
					break stream // the stream broke; reconnect
				}
				if !triggers[ev.Name] {
					continue // skips the idle check: most events are not ours
				}
				if blocks, err := d.queuedBlocks(); err == nil && blocks[ev.Block] {
					d.Block(ctx, ev.Block)
				}
			}
			if w.done() {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return nil
}

// watch holds watch.lock for one Watch call.
type watch struct {
	d         *Deliverer
	untilIdle bool
	lock      *store.WatcherLock
}

// hold takes watch.lock. A watcher started by a command gives way to one
// already running; one started by hand waits its turn.
func (w *watch) hold(ctx context.Context) bool {
	for {
		l, err := w.d.Store.HoldWatcher()
		if err == nil {
			w.lock = l
			return true
		}
		if w.untilIdle {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

func (w *watch) release() {
	if w.lock != nil {
		w.lock.Release()
		w.lock = nil
	}
}

// done reports whether an until-idle watcher should exit. It releases the
// lock and then checks once more: a command writes its hold before it
// tests the lock, so a message held during the release is seen here.
func (w *watch) done() bool {
	if !w.untilIdle || w.needed() {
		return false
	}
	w.release()
	if !w.needed() {
		return true
	}
	l, err := w.d.Store.HoldWatcher()
	if err != nil {
		return true // a new watcher took over
	}
	w.lock = l
	return false
}

// needed treats a store it cannot read as needing the watcher.
func (w *watch) needed() bool {
	n, err := w.d.NeedsWatcher()
	return err != nil || n
}

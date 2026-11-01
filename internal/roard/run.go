package roard

import (
	"context"
	"errors"
	"time"

	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/store"
)

// Run is roard: it returns nil at once when another roard holds the store,
// and otherwise runs until ctx ends.
func Run(ctx context.Context, d Deps) error {
	if d.Label == "" {
		d.Label = "roard"
	}
	lock, err := d.Store.HoldDaemon()
	if errors.Is(err, store.ErrBusy) {
		d.logf("another roard holds %s; exiting", d.Store.Root())
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runCtx := ctx

	s := d.Settings()
	p := newPool(s.Int("roard.workers"))
	defer p.stop()
	cb := newCallbacks(ctx, d)
	sb := newSubs(cb)
	w := newWatcher(ctx, d, sb, p)
	defer w.stop()
	up, err := newUploads(d)
	if err != nil {
		return err
	}
	defer up.stop()
	srv := &server{d: d, subs: sb, cb: cb, pool: p, w: w, up: up, ctx: ctx}
	rl := &relay{d: d}
	var sess *rexctl.Session
	onEvent := func(e rexctl.Event) {
		w.rexEvent(e)
		if (e.Type == "block_closed" || e.Type == "session_destroyed") && rl.known() {
			p.submit(func() {
				if c := sess.Current(); c != nil {
					rl.check(ctx, c)
				}
			})
		}
	}

	sess = rexctl.Start(ctx, rexctl.SessionOptions{
		Options: rexctl.Options{
			Endpoint:  d.Endpoint,
			Info:      rexctl.Info{Kind: "other", Name: "roard", Version: d.Version, Label: d.Label},
			OnEvent:   onEvent,
			OnPerform: srv.perform,
		},
		Backoff:     s.Backoff("roard.rex.reconnect"),
		DialTimeout: 10 * time.Second,
		Setup: func(ctx context.Context, c *rexctl.Client) error {
			if err := c.Declare(ctx, d.Label, actions); err != nil {
				return err
			}
			// Scope all, because block_event carries agent status. Events
			// missed while down are not replayed, so reconcile.
			if err := c.Call(ctx, "session.attach_all", map[string]string{"event_scope": "all"}, nil); err != nil {
				return err
			}
			go w.reconcile()
			go rl.check(runCtx, c) // not ctx: that one ends when Setup returns
			d.logf("connected to %s as %s", d.Endpoint, c.ID())
			return nil
		},
		OnRetry: func(err error, delay time.Duration) {
			d.logf("rex connection: %v; retrying in %v", err, delay.Round(time.Millisecond))
		},
	})
	cb.sess.Store(sess)
	if err := w.start(); err != nil {
		cancel()
		<-sess.Done()
		return err
	}
	<-sess.Done()
	return nil
}

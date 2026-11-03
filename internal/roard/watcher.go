package roard

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/store"
)

// triggers are the terminal events after which a held message may be
// deliverable: the agent's status changed, or its process did.
var triggers = map[string]bool{
	"program_status_changed": true, "program_status_removed": true,
	"process_changed": true, "child_exited": true,
}

// covered are hold reasons that a trigger reports the end of. Every other
// hold, such as a draft in the composer, may clear with no event, so the
// gap re-check covers it.
// A message held behind another waits on that one, whose own reason decides
// whether the gap re-check runs.
var covered = map[string]bool{"busy": true, "behind": true}

// watcher delivers held messages as events arrive: notify marks from the
// store, and Rex events from roard's own connection. It sets a timer only
// for work that no event covers.
type watcher struct {
	ctx  context.Context
	d    Deps
	subs *subs
	pool *pool

	mu      sync.Mutex
	pending map[string]bool // panes whose queued messages need the watcher
	gaps    map[string]bool // panes whose holds no event reports
	timers  map[string]*time.Timer
	events  map[string]uint64 // trigger events seen per pane, to catch one during an attempt
	gap     *time.Timer
	sweep   *time.Timer
	locked  bool
	lock    *store.WatcherLock
	retry   *time.Timer
	fsw     *fsnotify.Watcher
	kick    chan struct{}
	stopped bool
}

func newWatcher(ctx context.Context, d Deps, s *subs, p *pool) *watcher {
	return &watcher{ctx: ctx, d: d, subs: s, pool: p, pending: map[string]bool{}, gaps: map[string]bool{},
		timers: map[string]*time.Timer{}, events: map[string]uint64{}, kick: make(chan struct{}, 1)}
}

// start watches the notify directory, then takes watch.lock and reconciles.
// It subscribes before it reads, so no change falls between.
func (w *watcher) start() error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := fsw.Add(w.d.Store.NotifyDir()); err != nil {
		fsw.Close()
		return err
	}
	w.fsw = fsw
	go w.loop()
	w.takeLock()
	w.wake()
	return nil
}

func (w *watcher) loop() {
	for {
		select {
		case _, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.wake()
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.d.logf("store watch: %v; reconciling", err) // an overflow may have lost marks
			w.reconcile()
		case <-w.kick:
			w.drain()
		case <-w.ctx.Done():
			return
		}
	}
}

func (w *watcher) wake() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

func (w *watcher) stop() {
	w.mu.Lock()
	w.stopped = true
	for _, t := range w.timers {
		t.Stop()
	}
	for _, t := range []*time.Timer{w.gap, w.sweep, w.retry} {
		if t != nil {
			t.Stop()
		}
	}
	if w.lock != nil {
		w.lock.Release()
	}
	w.mu.Unlock()
	if w.fsw != nil {
		w.fsw.Close()
	}
}

// takeLock holds watch.lock for roard's life. An old watcher block may still
// hold it; flock release has no event, so roard retries until it gets it.
func (w *watcher) takeLock() {
	l, err := w.d.Store.HoldWatcher()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		if err == nil {
			l.Release()
		}
		return
	}
	if err != nil {
		w.retry = time.AfterFunc(w.d.Settings().Duration("watcher.legacy_lock_retry"), w.takeLock)
		return
	}
	w.lock, w.locked, w.retry = l, true, nil
	go w.reconcile()
}

// haveLock is for tests that drive the watcher without starting it.
func (w *watcher) haveLock(v bool) {
	w.mu.Lock()
	w.locked = v
	w.mu.Unlock()
}

// drain reads every notify mark and acts on the messages they name.
func (w *watcher) drain() {
	ids, err := w.d.Store.TakeNotified()
	if err != nil {
		w.d.logf("notify: %v", err)
		return
	}
	blocks := map[string]bool{}
	for _, id := range ids {
		m, err := w.d.Store.Get(id)
		if err != nil {
			continue
		}
		w.subs.publish(m)
		blocks[m.Envelope.To.Block] = true
	}
	for b := range blocks {
		w.cover(b)
	}
}

// reconcile reads every queued pane: on start, after a reconnect, and after
// the store watch reports an error.
func (w *watcher) reconcile() {
	w.d.Store.SettleOrphans(time.Now())
	blocks, err := w.d.Store.QueuedBlocks()
	if err != nil {
		w.d.logf("reconcile: %v", err)
		return
	}
	for b := range blocks {
		w.cover(b)
	}
	for _, id := range w.subs.openIDs() {
		if m, err := w.d.Store.Get(id); err == nil {
			w.subs.publish(m)
		}
	}
}

// cover makes sure a pane's queued messages are attempted.
func (w *watcher) cover(block string) {
	w.refresh(block)
	w.schedule(block)
}

// schedule attempts a pane once after the coalesce window, so a burst of
// events causes one attempt.
func (w *watcher) schedule(block string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.locked || w.stopped || !w.pending[block] || w.timers[block] != nil {
		return
	}
	w.timers[block] = time.AfterFunc(w.d.Settings().Duration("watcher.coalesce"), func() {
		w.mu.Lock()
		delete(w.timers, block)
		w.mu.Unlock()
		err := w.pool.submit(func() {
			seen := w.eventCount(block)
			w.d.Delivery.Block(w.ctx, block)
			w.refresh(block)
			// An event during the attempt may report what the attempt read
			// too early, and the attempt that event caused may have lost the
			// message lock to this one. So look once more.
			if w.eventCount(block) != seen {
				w.schedule(block)
			}
		})
		if err != nil {
			w.d.logf("attempt %s: %v", block, err)
		}
	})
}

// rexEvent routes one event from roard's connection. A session's events,
// block_event and block_closed among them, arrive inside a session_event;
// server events such as client_left arrive bare.
func (w *watcher) rexEvent(e rexctl.Event) {
	if e.Type == "session_event" {
		var s struct {
			Event struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			} `json:"event"`
		}
		if json.Unmarshal(e.Payload, &s) != nil {
			return
		}
		e = rexctl.Event{Seq: e.Seq, Type: s.Event.Type, Payload: s.Event.Payload}
	}
	var p struct {
		BlockID  string `json:"block_id"`
		ClientID string `json:"client_id"`
		Name     string `json:"name"`
	}
	switch e.Type {
	case "block_event", "block_closed", "client_left":
		if json.Unmarshal(e.Payload, &p) != nil {
			return
		}
	default:
		return
	}
	switch {
	case e.Type == "client_left":
		w.subs.drop(p.ClientID)
	case e.Type == "block_closed", triggers[p.Name]:
		w.mu.Lock()
		pending := w.pending[p.BlockID]
		if pending {
			w.events[p.BlockID]++
		}
		w.mu.Unlock()
		if e.Type == "block_closed" {
			w.d.logf("block_closed %s pending=%v", p.BlockID, pending)
		}
		if pending {
			w.schedule(p.BlockID)
		}
	}
}

func (w *watcher) eventCount(block string) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.events[block]
}

package roard

import (
	"time"

	"github.com/vizkid/roar/internal/store"
)

// refresh recomputes one pane's pending and gap state, and the gap timer.
func (w *watcher) refresh(block string) {
	msgs, _ := w.d.Store.Inbox(block)
	adapter := w.d.Store.AdapterLive(block)
	pending, gap := false, false
	for _, m := range msgs {
		if m.Record.State != store.Queued {
			continue
		}
		if adapter {
			gap = true // the adapter may die, and a released flock has no event
			continue
		}
		pending = true
		if m.Record.HoldReason != "" && !covered[m.Record.HoldReason] {
			gap = true
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	setFlag(w.pending, block, pending)
	setFlag(w.gaps, block, gap)
	w.armGap()
}

func setFlag(m map[string]bool, k string, v bool) {
	if v {
		m[k] = true
	} else {
		delete(m, k)
	}
}

// armGap runs a timer only while some work has no event. Called with mu.
func (w *watcher) armGap() {
	need := len(w.gaps) > 0 || len(w.subs.openIDs()) > 0
	switch {
	case need && w.gap == nil && !w.stopped:
		w.gap = time.AfterFunc(w.d.Settings().Duration("watcher.gap_recheck"), w.gapFire)
	case !need && w.gap != nil:
		w.gap.Stop()
		w.gap = nil
	}
}

func (w *watcher) gapArmed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gap != nil
}

func (w *watcher) gapFire() {
	w.mu.Lock()
	w.gap = nil
	blocks := make([]string, 0, len(w.gaps))
	for b := range w.gaps {
		blocks = append(blocks, b)
	}
	w.mu.Unlock()
	for _, b := range blocks {
		w.cover(b)
	}
	for _, id := range w.subs.openIDs() {
		if m, err := w.d.Store.Get(id); err == nil {
			w.subs.publish(m)
		}
	}
	w.mu.Lock()
	w.armGap()
	w.mu.Unlock()
}

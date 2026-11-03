package roard

import (
	"context"
	"sync"
	"time"
)

// queue is a FIFO a single consumer waits on.
type queue[T any] struct {
	mu     sync.Mutex
	items  []T
	signal chan struct{}
}

func newQueue[T any]() queue[T] { return queue[T]{signal: make(chan struct{}, 1)} }

func (q *queue[T]) wake() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *queue[T]) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// pop takes the oldest item. It reports false when ctx ends, or when the
// queue stays empty for idle; idle 0 waits for ctx alone.
func (q *queue[T]) pop(ctx context.Context, idle time.Duration) (T, bool) {
	var zero T
	var timeout <-chan time.Time
	if idle > 0 {
		t := time.NewTimer(idle)
		defer t.Stop()
		timeout = t.C
	}
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			v := q.items[0]
			q.items[0] = zero // the backing array must not keep the payload
			q.items = q.items[1:]
			q.mu.Unlock()
			return v, true
		}
		q.mu.Unlock()
		select {
		case <-q.signal:
		case <-timeout:
			return zero, false
		case <-ctx.Done():
			return zero, false
		}
	}
}

// add queues r. Queued subscription updates for one message merge into the
// newer: the sender only needs the newest, and it goes to the back so
// revisions stay in order. Publishers send outside the subscription lock,
// so an older update can arrive second; it is then dropped, not kept. When
// max items are queued, the oldest is dropped and add reports true. max 0
// means no bound.
func (sq *senderQueue) add(r Result, max int) (dropped bool) {
	sq.mu.Lock()
	if r.Subscription != "" && len(r.Rows) == 1 {
		for i, old := range sq.items {
			if old.Subscription == r.Subscription && len(old.Rows) == 1 && old.Rows[0].ID == r.Rows[0].ID {
				if newer(old, r) {
					sq.mu.Unlock()
					return false
				}
				sq.items = append(sq.items[:i], sq.items[i+1:]...)
				break
			}
		}
	}
	if max > 0 && len(sq.items) >= max {
		sq.items[0] = Result{}
		sq.items = sq.items[1:]
		dropped = true
	}
	sq.items = append(sq.items, r)
	sq.mu.Unlock()
	sq.wake()
	return dropped
}

// newer reports whether a is a later update than b for the same message:
// by the message's record revision, then by the subscription's.
func newer(a, b Result) bool {
	if ra, rb := a.Rows[0].Revision, b.Rows[0].Revision; ra != rb {
		return ra > rb
	}
	return a.Revision > b.Revision
}

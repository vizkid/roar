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

// add queues r. A queued subscription update for the same message is
// replaced: the sender only needs the newest, and the new one goes to the
// back so revisions stay in order. When max items are queued, the oldest
// is dropped and add reports true. max 0 means no bound.
func (sq *senderQueue) add(r Result, max int) (dropped bool) {
	sq.mu.Lock()
	if r.Subscription != "" && len(r.Rows) == 1 {
		for i, old := range sq.items {
			if old.Subscription == r.Subscription && len(old.Rows) == 1 && old.Rows[0].ID == r.Rows[0].ID {
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

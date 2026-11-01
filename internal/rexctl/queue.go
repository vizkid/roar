package rexctl

import "sync"

// queue hands items from the reader to one consumer without ever blocking
// the reader. Rex closes a connection whose unread events pass 8 MiB, so a
// slow consumer must not stall reads.
type queue[T any] struct {
	mu     sync.Mutex
	items  []T
	signal chan struct{}
}

func newQueue[T any]() *queue[T] { return &queue[T]{signal: make(chan struct{}, 1)} }

func (q *queue[T]) push(v T) {
	q.mu.Lock()
	q.items = append(q.items, v)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// run calls fn for each item, in order, until done closes.
func (q *queue[T]) run(done <-chan struct{}, fn func(T)) {
	for {
		select {
		case <-q.signal:
		case <-done:
			return
		}
		for {
			q.mu.Lock()
			if len(q.items) == 0 {
				q.mu.Unlock()
				break
			}
			v := q.items[0]
			var zero T
			q.items[0] = zero
			q.items = q.items[1:]
			q.mu.Unlock()
			fn(v)
		}
	}
}

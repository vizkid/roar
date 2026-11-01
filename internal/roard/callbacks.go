package roard

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/rexctl"
)

// callbacks calls roar.result on senders, one queue per sender so results
// arrive in order. It never runs inside an action handler: a call in each
// direction, each waiting on the other's queue, would deadlock.
type callbacks struct {
	ctx  context.Context
	d    Deps
	sess atomic.Pointer[rexctl.Session]
	mu   sync.Mutex
	q    map[string]*senderQueue
}

type senderQueue struct {
	queue[Result]
	hints Hints
	rtt   config.RTT
}

func newCallbacks(ctx context.Context, d Deps) *callbacks {
	return &callbacks{ctx: ctx, d: d, q: map[string]*senderQueue{}}
}

func (c *callbacks) send(client string, r Result, h Hints) {
	c.mu.Lock()
	sq := c.q[client]
	if sq == nil {
		s := c.d.Settings()
		sq = &senderQueue{queue: newQueue[Result](), rtt: config.RTT{Alpha: s.Float("sender.call.rtt_alpha")}}
		c.q[client] = sq
		go c.run(client, sq)
	}
	if h != (Hints{}) {
		sq.hints = h
	}
	c.mu.Unlock()
	sq.push(r)
}

func (c *callbacks) run(client string, sq *senderQueue) {
	for {
		r, ok := sq.pop(c.ctx)
		if !ok {
			return
		}
		if !c.deliver(client, sq, r) {
			c.mu.Lock()
			delete(c.q, client)
			c.mu.Unlock()
			return // the sender left; its resubscribe will read the store
		}
	}
}

// deliver calls back until it works, the sender is gone, or give_up passes.
// It reports false when the sender is gone.
func (c *callbacks) deliver(client string, sq *senderQueue, r Result) bool {
	s := c.d.Settings()
	c.mu.Lock()
	h := sq.hints
	c.mu.Unlock()
	giveUp := s.Duration("roard.callback.give_up")
	if h.CallbackGiveUpMS > 0 {
		giveUp = min(time.Duration(h.CallbackGiveUpMS)*time.Millisecond, s.Duration("roard.callback.max_give_up"))
	}
	dl := s.Deadline("roard.callback")
	if h.CallbackTimeoutMS > 0 {
		dl.Timeout = min(time.Duration(h.CallbackTimeoutMS)*time.Millisecond, s.Duration("roard.callback.max_timeout"))
	}
	retry := config.NewRetry(s.Backoff("roard.callback"), nil)
	start := time.Now()
	for {
		sess := c.sess.Load()
		if sess == nil {
			return true
		}
		cl, err := sess.Client(c.ctx)
		if err != nil {
			return true // roard is stopping
		}
		ctx, cancel := context.WithTimeout(c.ctx, dl.For(sq.rtt.SRTT()))
		t0 := time.Now()
		err = cl.Perform(ctx, client, "roar.result", r, min(dl.Timeout, 30*time.Second), nil)
		cancel()
		if err == nil {
			sq.rtt.Observe(time.Since(t0))
			return true
		}
		if rexctl.Code(err) == "not_found" {
			return false
		}
		if c.ctx.Err() != nil {
			return true
		}
		if time.Since(start) >= giveUp {
			c.d.logf("callback to %s: giving up after %v: %v", client, time.Since(start).Round(time.Second), err)
			return true
		}
		t := time.NewTimer(retry.Fail(time.Now()))
		select {
		case <-t.C:
		case <-c.ctx.Done():
			t.Stop()
			return true
		}
	}
}

// queue is a FIFO a single consumer waits on.
type queue[T any] struct {
	mu     sync.Mutex
	items  []T
	signal chan struct{}
}

func newQueue[T any]() queue[T] { return queue[T]{signal: make(chan struct{}, 1)} }

func (q *queue[T]) push(v T) {
	q.mu.Lock()
	q.items = append(q.items, v)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *queue[T]) pop(ctx context.Context) (T, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			v := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return v, true
		}
		q.mu.Unlock()
		select {
		case <-q.signal:
		case <-ctx.Done():
			var zero T
			return zero, false
		}
	}
}

package roard

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vizkid/rexkit/rexctl"
	"github.com/vizkid/roar/internal/config"
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

// senderQueue is one sender's results and their consumer. Its context ends
// when the sender leaves, so its retries stop with it.
type senderQueue struct {
	queue[Result]
	hints  Hints
	rtt    config.RTT
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the consumer returns
}

func newCallbacks(ctx context.Context, d Deps) *callbacks {
	return &callbacks{ctx: ctx, d: d, q: map[string]*senderQueue{}}
}

// send queues r for client. It adds under c.mu, so a consumer that ends
// for being idle cannot miss a result added at the same moment.
func (c *callbacks) send(client string, r Result, h Hints) {
	s := c.d.Settings()
	c.mu.Lock()
	defer c.mu.Unlock()
	sq := c.q[client]
	if sq == nil {
		ctx, cancel := context.WithCancel(c.ctx)
		sq = &senderQueue{queue: newQueue[Result](), rtt: config.RTT{Alpha: s.Float("sender.call.rtt_alpha")},
			ctx: ctx, cancel: cancel, done: make(chan struct{})}
		c.q[client] = sq
		go c.run(client, sq)
	}
	if h != (Hints{}) {
		sq.hints = h
	}
	if sq.add(r, s.Int("roard.callback.max_queued")) {
		c.d.logf("callback queue for %s is full; dropped its oldest result", client)
	}
}

// drop ends a departed sender's queue and its retries.
func (c *callbacks) drop(client string) {
	c.mu.Lock()
	sq := c.q[client]
	delete(c.q, client)
	c.mu.Unlock()
	if sq != nil {
		sq.cancel()
	}
}

// run delivers client's results in order. It ends when the sender leaves,
// when a callback finds it gone, when roard stops, or after the queue has
// been empty for roard.callback.idle: a client_left that came while roard
// was disconnected is never seen.
func (c *callbacks) run(client string, sq *senderQueue) {
	defer close(sq.done)
	defer sq.cancel()
	idle := c.d.Settings().Duration("roard.callback.idle")
	for {
		r, ok := sq.pop(sq.ctx, idle)
		if !ok {
			if sq.ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			if sq.size() == 0 && c.q[client] == sq {
				delete(c.q, client)
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			continue
		}
		if !c.deliver(client, sq, r) {
			c.mu.Lock()
			if c.q[client] == sq {
				delete(c.q, client)
			}
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
		cl, err := sess.Client(sq.ctx)
		if err != nil {
			return true // roard is stopping, or the sender left
		}
		ctx, cancel := context.WithTimeout(sq.ctx, dl.For(sq.rtt.SRTT()))
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
		if sq.ctx.Err() != nil {
			return true
		}
		if time.Since(start) >= giveUp {
			c.d.logf("callback to %s: giving up after %v: %v", client, time.Since(start).Round(time.Second), err)
			return true
		}
		t := time.NewTimer(retry.Fail(time.Now()))
		select {
		case <-t.C:
		case <-sq.ctx.Done():
			t.Stop()
			return true
		}
	}
}

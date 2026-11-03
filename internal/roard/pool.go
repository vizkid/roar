package roard

import (
	"errors"
	"sync"
)

var errBusy = errors.New("roard_busy: too much work queued")

// pool runs work off the Rex reader and off the action handlers, which must
// answer at once: Rex hands roard one request at a time.
type pool struct {
	ch     chan func()
	wg     sync.WaitGroup
	mu     sync.Mutex // orders submit's send against stop's close
	closed bool
}

// poolQueue bounds queued work. A full queue refuses new work instead of
// blocking a handler.
const poolQueue = 1024

func newPool(workers int) *pool {
	p := &pool{ch: make(chan func(), poolQueue)}
	for range max(workers, 1) {
		p.wg.Go(func() {
			for f := range p.ch {
				f()
			}
		})
	}
	return p
}

// submit queues f, or refuses when the queue is full or the pool stopped.
// The send never blocks, so holding mu for it is cheap.
func (p *pool) submit(f func()) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errBusy
	}
	select {
	case p.ch <- f:
		return nil
	default:
		return errBusy
	}
}

// stop refuses new work, runs what is queued, and waits for the workers.
func (p *pool) stop() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.ch)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

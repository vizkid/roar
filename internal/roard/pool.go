package roard

import (
	"errors"
	"sync"
)

var errBusy = errors.New("roard_busy: too much work queued")

// pool runs work off the Rex reader and off the action handlers, which must
// answer at once: Rex hands roard one request at a time.
type pool struct {
	ch   chan func()
	wg   sync.WaitGroup
	once sync.Once
}

// poolQueue bounds queued work. A full queue refuses new work instead of
// blocking a handler.
const poolQueue = 1024

func newPool(workers int) *pool {
	p := &pool{ch: make(chan func(), poolQueue)}
	for range max(workers, 1) {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for f := range p.ch {
				f()
			}
		}()
	}
	return p
}

func (p *pool) submit(f func()) (err error) {
	defer func() {
		if recover() != nil {
			err = errBusy // stopped
		}
	}()
	select {
	case p.ch <- f:
		return nil
	default:
		return errBusy
	}
}

func (p *pool) stop() {
	p.once.Do(func() { close(p.ch) })
	p.wg.Wait()
}

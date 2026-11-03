package roard

import (
	"sync"
	"testing"
)

// Review, 2026-11-03: stop closed the queue while submit could send on it,
// and a recovered panic hid the race. go test -race shows it.
func TestPoolStopWhileSubmitting(t *testing.T) {
	for range 50 {
		p := newPool(2, 64)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for range 100 {
					p.submit(func() {})
				}
			})
		}
		p.stop()
		wg.Wait()
		if p.submit(func() {}) == nil {
			t.Fatal("a stopped pool took work")
		}
	}
}

package cli

import (
	"context"
	"os"
	"time"
)

// ReplacedEvery is how often a watcher checks whether `roar install` or a
// rebuild replaced its binary. A watcher runs for days; without this it
// would keep delivering with the code it started with.
const ReplacedEvery = 30 * time.Second

// binary is the file a process started from, as it was then.
type binary struct {
	path string
	info os.FileInfo
}

func currentBinary() (binary, bool) {
	path, err := os.Executable()
	if err != nil {
		return binary{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return binary{}, false
	}
	return binary{path, info}, true
}

// replaced reports a new file at the path, or the same file rewritten.
// A missing file is not replaced: an install in progress may have removed
// it for a moment.
func (b binary) replaced() bool {
	now, err := os.Stat(b.path)
	if err != nil {
		return false
	}
	return !os.SameFile(b.info, now) || !now.ModTime().Equal(b.info.ModTime()) || now.Size() != b.info.Size()
}

// untilReplaced returns a context that ends when ctx ends or the binary is
// replaced, and a function that reports which happened.
func untilReplaced(ctx context.Context, b binary) (context.Context, func() bool) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan bool, 1)
	go func() {
		t := time.NewTicker(ReplacedEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- false
				return
			case <-t.C:
				if b.replaced() {
					done <- true
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() bool { cancel(); return <-done }
}

package rexctl

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/config"
)

// A dial that keeps failing waits exactly the backoff's delays.
func TestSessionBacksOffOnDialFailure(t *testing.T) {
	var mu sync.Mutex
	var delays []time.Duration
	ctx, cancel := context.WithCancel(t.Context())
	s := Start(ctx, SessionOptions{
		Options:     Options{Endpoint: "unix://" + filepath.Join(t.TempDir(), "none.sock")},
		Backoff:     config.Backoff{Initial: 10 * time.Millisecond, Factor: 2, Max: 40 * time.Millisecond},
		DialTimeout: time.Second,
		OnRetry: func(err error, d time.Duration) {
			mu.Lock()
			delays = append(delays, d)
			if len(delays) == 5 {
				cancel()
			}
			mu.Unlock()
		},
	})
	<-s.Done()
	want := []time.Duration{10, 20, 40, 40, 40}
	for i, w := range want {
		if delays[i] != w*time.Millisecond {
			t.Fatalf("delays %v", delays)
		}
	}
	if c := s.Current(); c != nil {
		t.Fatal("a failed session has a client")
	}
}

// A dropped connection reconnects and runs Setup again.
func TestSessionReconnectsAndRunsSetup(t *testing.T) {
	ep := local(t)
	var setups atomic.Int32
	s := Start(t.Context(), SessionOptions{
		Options:     Options{Endpoint: ep, Info: Info{Kind: "other", Name: "roar-test"}},
		Backoff:     config.Backoff{Initial: 10 * time.Millisecond, Factor: 2, Max: 100 * time.Millisecond},
		DialTimeout: 5 * time.Second,
		Setup:       func(ctx context.Context, c *Client) error { setups.Add(1); return nil },
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first, err := s.Client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first.conn.Close() // the network drops, not a Close by us
	deadline := time.Now().Add(10 * time.Second)
	for setups.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("setup ran %d times", setups.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	second, err := s.Client(ctx)
	if err != nil || second == first {
		t.Fatalf("second client %v: %v", second, err)
	}
	if err := second.Call(ctx, "server.status", nil, nil); err != nil {
		t.Fatal(err)
	}
}

// A Setup that fails drops the connection and backs off like a dial error.
func TestSessionSetupFailureBacksOff(t *testing.T) {
	ep := local(t)
	var tries atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	s := Start(ctx, SessionOptions{
		Options:     Options{Endpoint: ep},
		Backoff:     config.Backoff{Initial: 10 * time.Millisecond, Factor: 1, Max: 10 * time.Millisecond},
		DialTimeout: 5 * time.Second,
		Setup: func(context.Context, *Client) error {
			if tries.Add(1) == 3 {
				cancel()
			}
			return context.DeadlineExceeded
		},
	})
	<-s.Done()
	if tries.Load() != 3 {
		t.Fatalf("tries %d", tries.Load())
	}
}

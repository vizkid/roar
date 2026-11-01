package rexctl

import (
	"context"
	"sync"
	"time"

	"github.com/vizkid/roar/internal/config"
)

// SessionOptions configures a connection that reconnects by itself.
type SessionOptions struct {
	Options
	Backoff     config.Backoff
	DialTimeout time.Duration
	// Setup runs after every connect, before Client returns the new
	// connection: declare actions, subscribe, reconcile. An error drops the
	// connection and counts as a failure.
	Setup func(ctx context.Context, c *Client) error
	// OnRetry reports each failure and the delay before the next try.
	OnRetry func(err error, delay time.Duration)
	// Rand replaces the backoff's random source, for tests.
	Rand func() float64
}

// Session keeps one control connection up. It never retries faster than its
// backoff, and it ends when ctx ends.
type Session struct {
	o     SessionOptions
	retry *config.Retry
	mu    sync.Mutex
	cur   *Client
	ready chan struct{} // closed while cur is set
	done  chan struct{}
}

// Start begins connecting and returns at once.
func Start(ctx context.Context, o SessionOptions) *Session {
	s := &Session{o: o, retry: config.NewRetry(o.Backoff, o.Rand), ready: make(chan struct{}), done: make(chan struct{})}
	go s.loop(ctx)
	return s
}

// Done closes once the session has stopped for good.
func (s *Session) Done() <-chan struct{} { return s.done }

// SetBackoff applies a new backoff from a config reload.
func (s *Session) SetBackoff(b config.Backoff) {
	s.mu.Lock()
	s.retry.SetBackoff(b)
	s.mu.Unlock()
}

// Current is the live connection, or nil while down.
func (s *Session) Current() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Client waits for a live connection.
func (s *Session) Client(ctx context.Context) (*Client, error) {
	for {
		s.mu.Lock()
		cur, ready := s.cur, s.ready
		s.mu.Unlock()
		if cur != nil {
			return cur, nil
		}
		select {
		case <-ready:
		case <-s.done:
			return nil, ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *Session) loop(ctx context.Context) {
	defer close(s.done)
	token := s.o.ReconnectToken
	for ctx.Err() == nil {
		c, err := s.connect(ctx, token)
		if err != nil {
			if token != "" {
				token = "" // the server may have forgotten it; start a new client
			}
			if !s.wait(ctx, err) {
				return
			}
			continue
		}
		token = c.ReconnectToken()
		s.mu.Lock()
		s.retry.Up(time.Now())
		s.cur = c
		close(s.ready)
		s.mu.Unlock()
		select {
		case <-c.Done():
		case <-ctx.Done():
			c.Close()
		}
		s.mu.Lock()
		s.cur = nil
		s.ready = make(chan struct{})
		s.mu.Unlock()
		if ctx.Err() != nil || !s.wait(ctx, c.Err()) {
			return
		}
	}
}

func (s *Session) connect(ctx context.Context, token string) (*Client, error) {
	o := s.o.Options
	o.ReconnectToken = token
	dctx, cancel := context.WithTimeout(ctx, s.o.DialTimeout)
	defer cancel()
	c, err := Dial(dctx, o)
	if err != nil {
		return nil, err
	}
	if s.o.Setup != nil {
		if err := s.o.Setup(dctx, c); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

// wait sleeps for the next backoff delay. It reports false once ctx ends.
func (s *Session) wait(ctx context.Context, err error) bool {
	s.mu.Lock()
	d := s.retry.Fail(time.Now())
	s.mu.Unlock()
	if s.o.OnRetry != nil {
		s.o.OnRetry(err, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

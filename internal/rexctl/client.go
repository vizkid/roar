package rexctl

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Options configures one connection. OnEvent and OnPerform each run on a
// goroutine of their own, one call at a time, so they may block without
// stalling reads.
type Options struct {
	Endpoint       string // unix:///path, https://host or http://host:port
	Info           Info
	ReconnectToken string
	OnEvent        func(Event)
	OnPerform      func(Perform) (any, *Error)
}

// Client is one open control connection.
type Client struct {
	conn    *websocket.Conn
	id      string
	token   string
	max     int
	opts    Options
	writeMu sync.Mutex
	mu      sync.Mutex
	next    int64
	pending map[string]chan response
	done    chan struct{}
	err     error
	once    sync.Once
}

type response struct {
	Payload json.RawMessage `json:"payload"`
	Error   *Error          `json:"error"`
}

func (c *Client) ID() string             { return c.id }
func (c *Client) ReconnectToken() string { return c.token }
func (c *Client) MaxMessage() int        { return c.max }
func (c *Client) Done() <-chan struct{}  { return c.done }

// Close says goodbye with a close frame, so Rex removes this client at once
// instead of keeping it as lost for 60 s.
func (c *Client) Close() {
	// WriteControl may run beside a write in progress, so a send stuck on a
	// stalled link cannot hold Close up past this deadline.
	c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	c.fail(ErrClosed)
}

func (c *Client) Endpoint() string { return c.opts.Endpoint }

// Err is why the connection ended, once Done is closed.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		c.conn.Close()
		close(c.done)
	})
}

func (c *Client) read() {
	events, performs := newQueue[Event](), newQueue[Perform]()
	if c.opts.OnEvent != nil {
		go events.run(c.done, c.opts.OnEvent)
	}
	go performs.run(c.done, c.perform)
	for {
		var m struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Seq     int64           `json:"seq"`
			Payload json.RawMessage `json:"payload"`
			Error   *Error          `json:"error"`
		}
		if err := c.conn.ReadJSON(&m); err != nil {
			c.fail(fmt.Errorf("rex control read: %w", err))
			return
		}
		switch m.Type {
		case "response":
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- response{Payload: m.Payload, Error: m.Error}
			}
		case "event":
			var e struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(m.Payload, &e) != nil {
				continue
			}
			if e.Type == "client_perform_requested" {
				var p Perform
				if json.Unmarshal(e.Payload, &p) == nil {
					performs.push(p)
				}
				continue
			}
			if c.opts.OnEvent != nil {
				events.push(Event{Seq: m.Seq, Type: e.Type, Payload: e.Payload})
			}
		}
	}
}

func (c *Client) send(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

// Call runs one method and decodes its result into out, which may be nil.
// A failed call returns *Error.
func (c *Client) Call(ctx context.Context, method string, payload, out any) error {
	wait, err := c.Start(method, payload)
	if err != nil {
		return err
	}
	return wait(ctx, out)
}

// Start writes one request and returns at once with a function that waits
// for its response. Requests go out in the order Start is called, so a
// caller can keep several in flight on one connection without reordering.
func (c *Client) Start(method string, payload any) (func(ctx context.Context, out any) error, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.next++
	id := strconv.FormatInt(c.next, 10)
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	msg, err := json.Marshal(map[string]any{"type": "request", "id": id, "method": method, "payload": payload})
	if err != nil {
		forget()
		return nil, err
	}
	// The server closes a connection that sends a message over its limit, so
	// refuse it here and keep the connection.
	if len(msg) > c.max {
		forget()
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(msg), c.max)
	}
	if err := c.send(json.RawMessage(msg)); err != nil {
		forget()
		c.fail(fmt.Errorf("rex control write: %w", err))
		return nil, c.Err()
	}
	return func(ctx context.Context, out any) error {
		defer forget()
		select {
		case r := <-ch:
			if r.Error != nil {
				return r.Error
			}
			if out != nil && len(r.Payload) > 0 {
				return json.Unmarshal(r.Payload, out)
			}
			return nil
		case <-c.done:
			return c.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil
}

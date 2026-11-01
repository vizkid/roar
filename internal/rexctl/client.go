package rexctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// LocalEndpoint is this host's own server: REX_SERVER, else Rex's default
// socket for the platform.
func LocalEndpoint(getenv func(string) string, home string) string {
	if s := getenv("REX_SERVER"); s != "" {
		return s
	}
	if runtime.GOOS == "darwin" {
		return "unix://" + filepath.Join(home, "Library", "Application Support", "rex", "server.sock")
	}
	dir := getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "rex-"+strconv.Itoa(os.Getuid()))
	}
	return "unix://" + filepath.Join(dir, "rex", "server.sock")
}

func dialer(endpoint string) (*websocket.Dialer, string, error) {
	d := &websocket.Dialer{Subprotocols: []string{protocol}, HandshakeTimeout: 0}
	if path, ok := strings.CutPrefix(endpoint, "unix://"); ok {
		p, err := url.PathUnescape(path)
		if err != nil {
			return nil, "", fmt.Errorf("rex endpoint %q: %w", endpoint, err)
		}
		d.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", p)
		}
		return d, "ws://localhost/api/control", nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, "", fmt.Errorf("rex endpoint %q: want unix://, https:// or http://", endpoint)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, "", fmt.Errorf("rex endpoint %q: want unix://, https:// or http://", endpoint)
	}
	u.Path = "/api/control"
	return d, u.String(), nil
}

// Dial opens the connection, sends the hello, and waits for the welcome.
// ctx bounds only the opening.
func Dial(ctx context.Context, o Options) (*Client, error) {
	d, target, err := dialer(o.Endpoint)
	if err != nil {
		return nil, err
	}
	conn, resp, err := d.DialContext(ctx, target, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("rex control %s: %w", o.Endpoint, err)
	}
	if conn.Subprotocol() != protocol {
		conn.Close()
		return nil, fmt.Errorf("rex control %s: server chose subprotocol %q", o.Endpoint, conn.Subprotocol())
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(dl)
		conn.SetWriteDeadline(dl)
	}
	hello := map[string]any{"type": "hello", "api_revision": apiRevision, "api_min_revision": apiRevision, "client": o.Info}
	if o.ReconnectToken != "" {
		hello["reconnect_token"] = o.ReconnectToken
	}
	if err := conn.WriteJSON(hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("rex control hello: %w", err)
	}
	var w struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
		Limits   struct {
			Max int `json:"max_message_bytes"`
		} `json:"limits"`
		Client struct {
			ID    string `json:"id"`
			Token string `json:"reconnect_token"`
		} `json:"client"`
	}
	if err := conn.ReadJSON(&w); err != nil || w.Type != "welcome" || w.Protocol != protocol {
		conn.Close()
		return nil, fmt.Errorf("rex control welcome: %v (type %q protocol %q)", err, w.Type, w.Protocol)
	}
	conn.SetReadDeadline(time.Time{})
	conn.SetWriteDeadline(time.Time{})
	c := &Client{conn: conn, id: w.Client.ID, token: w.Client.Token, max: max(w.Limits.Max, 64<<10),
		opts: o, pending: map[string]chan response{}, done: make(chan struct{})}
	conn.SetReadLimit(int64(c.max) + 4096)
	go c.read()
	return c, nil
}

func (c *Client) ID() string             { return c.id }
func (c *Client) ReconnectToken() string { return c.token }
func (c *Client) MaxMessage() int        { return c.max }
func (c *Client) Done() <-chan struct{}  { return c.done }
func (c *Client) Close()                 { c.fail(ErrClosed) }
func (c *Client) Endpoint() string       { return c.opts.Endpoint }

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
	if payload == nil {
		payload = map[string]any{}
	}
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.next++
	id := strconv.FormatInt(c.next, 10)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(map[string]any{"type": "request", "id": id, "method": method, "payload": payload}); err != nil {
		c.fail(fmt.Errorf("rex control write: %w", err))
		return c.Err()
	}
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
}

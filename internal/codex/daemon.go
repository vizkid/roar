// Package codex is Roar's native adapter for Codex. A plain Codex TUI is a
// client of its Codex home's shared App Server daemon, so Roar connects to
// that daemon as a second client and starts or steers turns on the thread
// the pane shows.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/gorilla/websocket"
)

// Socket is the control socket of a Codex home's shared daemon.
func Socket(home string) string {
	return filepath.Join(home, "app-server-control", "app-server-control.sock")
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

// errStale marks a call the thread's turn state made moot: a steer after
// the turn ended, or a start while one began. Nothing was sent.
var errStale = errors.New("stale turn")

var errClosed = errors.New("codex daemon connection closed")

type reply struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// client is one connection to the daemon. It ignores notifications.
type client struct {
	conn    *websocket.Conn
	write   sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[string]chan reply
	done    chan struct{}
}

// dial connects and initializes. It refuses a socket another user owns:
// whoever owns it would receive the message.
func dial(ctx context.Context, sock string) (*client, error) {
	fi, err := os.Stat(sock)
	if err != nil {
		return nil, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return nil, fmt.Errorf("%s is not owned by this user", sock)
	}
	d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	conn, resp, err := d.DialContext(ctx, "ws://localhost/", nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	c := &client{conn: conn, pending: map[string]chan reply{}, done: make(chan struct{})}
	go c.read()
	init := map[string]any{"clientInfo": map[string]string{"name": "roar", "version": "1"}}
	if err := c.call(ctx, "initialize", init, nil); err != nil {
		c.close()
		return nil, err
	}
	if err := c.send(map[string]any{"method": "initialized"}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *client) close() { c.conn.Close() }

func (c *client) send(v any) error {
	c.write.Lock()
	defer c.write.Unlock()
	return c.conn.WriteJSON(v)
}

func (c *client) read() {
	defer close(c.done)
	for {
		var m struct {
			ID json.RawMessage `json:"id"`
			reply
		}
		if err := c.conn.ReadJSON(&m); err != nil {
			return
		}
		var id string
		if json.Unmarshal(m.ID, &id) != nil {
			continue // a notification or a server request
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- m.reply
		}
	}
}

// call sends one request and decodes its result into out. An RPC error is
// an *rpcError: the daemon refused, so nothing happened.
func (c *client) call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	c.next++
	id := fmt.Sprintf("roar:%d", c.next)
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return r.Error
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	case <-c.done:
		return errClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// status reads the thread's runtime status: idle, active, notLoaded, or
// systemError.
func (c *client) status(ctx context.Context, thread string) (string, error) {
	var r struct {
		Thread struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	err := c.call(ctx, "thread/read", map[string]any{"threadId": thread}, &r)
	return r.Thread.Status.Type, err
}

// activeTurn returns the thread's newest in-progress turn, or "".
func (c *client) activeTurn(ctx context.Context, thread string) (string, error) {
	var r struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := c.call(ctx, "thread/turns/list", map[string]any{"threadId": thread, "limit": 5}, &r); err != nil {
		return "", err
	}
	for _, t := range r.Data {
		if t.Status == "inProgress" {
			return t.ID, nil
		}
	}
	return "", nil
}

// deliver starts a turn on an idle thread or steers an active one, and
// returns the method that carried the text.
func (c *client) deliver(ctx context.Context, thread, status, text string) (string, error) {
	input := []map[string]string{{"type": "text", "text": text}}
	method, params := "turn/start", map[string]any{"threadId": thread, "input": input}
	if status == "active" {
		turn, err := c.activeTurn(ctx, thread)
		if err != nil || turn == "" {
			return "turn/steer", fmt.Errorf("%w: no turn to steer: %v", errStale, err)
		}
		method, params["expectedTurnId"] = "turn/steer", turn
	}
	err := c.call(ctx, method, params, nil)
	var rerr *rpcError
	if errors.As(err, &rerr) && rerr.Code == -32600 {
		return method, fmt.Errorf("%w: %v", errStale, err)
	}
	return method, err
}

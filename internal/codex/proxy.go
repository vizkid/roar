// Package codex is Roar's native adapter for Codex: a WebSocket proxy
// between the Codex TUI and its App Server that injects turns into the
// thread the TUI shows.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// idPrefix marks the proxy's own requests. Their responses never reach the
// TUI, and a TUI frame using the prefix closes its connection.
const idPrefix = "roar:"

var (
	// ErrNotReady: no bound thread, or the TUI is switching threads. Nothing was sent.
	ErrNotReady = errors.New("codex_not_ready")
	// ErrRejected: Codex answered with an error. Nothing was delivered.
	ErrRejected = errors.New("codex_rejected")
	// ErrUnknown: the request was written but no answer came. The turn may have run.
	ErrUnknown = errors.New("codex_outcome_unknown")
)

// Receipt is Codex's acceptance of an injected turn.
type Receipt struct {
	Method string // turn/start or turn/steer
	Thread string
	Turn   string
}

// Proxy routes one pane's Codex TUI connections to its App Server.
type Proxy struct {
	upstream string
	mu       sync.Mutex // guards every link field and bound
	bound    *link
	seq      uint64
}

// NewProxy proxies to the App Server listening on the Unix socket upstream.
func NewProxy(upstream string) *Proxy { return &Proxy{upstream: upstream} }

type wire struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type reply struct {
	msg  wire
	lost bool // the connection closed first
}

// link is one TUI connection and its own App Server connection.
type link struct {
	up          *websocket.Conn
	write       sync.Mutex // serializes writes to up; taken before Proxy.mu
	initialized bool
	closed      bool
	thread      string
	turn        string
	threadCalls map[string]bool // the TUI's outstanding thread/start|resume|fork
	turnCalls   map[string]bool // the TUI's outstanding turn/start
	pending     map[string]chan reply
}

// Deliver starts a turn in the bound thread, or steers its active turn.
// Delivery takes the link's write lock before the proxy lock; the TUI's
// thread calls register under the proxy lock before writing. So a delivery
// either sees the switch coming and refuses, or is written before it.
func (p *Proxy) Deliver(ctx context.Context, text string) (Receipt, error) {
	p.mu.Lock()
	l := p.bound
	p.mu.Unlock()
	if l == nil {
		return Receipt{}, ErrNotReady
	}
	l.write.Lock()
	p.mu.Lock()
	if p.bound != l || l.closed || !l.initialized || l.thread == "" || len(l.threadCalls) > 0 {
		p.mu.Unlock()
		l.write.Unlock()
		return Receipt{}, ErrNotReady
	}
	r := Receipt{Method: "turn/start", Thread: l.thread, Turn: l.turn}
	params := map[string]any{"threadId": l.thread, "input": []map[string]string{{"type": "text", "text": text}}}
	if l.turn != "" {
		r.Method = "turn/steer"
		params["expectedTurnId"] = l.turn
	}
	p.seq++
	id := fmt.Sprintf("%s%d", idPrefix, p.seq)
	ch := make(chan reply, 1)
	l.pending[id] = ch
	p.mu.Unlock()

	l.up.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := l.up.WriteJSON(map[string]any{"id": id, "method": r.Method, "params": params})
	l.up.SetWriteDeadline(time.Time{})
	l.write.Unlock()
	if err != nil {
		p.forget(l, id)
		return r, fmt.Errorf("%w: write: %v", ErrUnknown, err)
	}
	select {
	case got := <-ch:
		if got.lost {
			return r, fmt.Errorf("%w: App Server connection closed", ErrUnknown)
		}
		if got.msg.Error != nil {
			if r.Method == "turn/steer" {
				p.mu.Lock()
				if l.turn == r.Turn {
					l.turn = "" // Codex says that turn is not active; start one next time
				}
				p.mu.Unlock()
			}
			return r, fmt.Errorf("%w: %d %s", ErrRejected, got.msg.Error.Code, got.msg.Error.Message)
		}
		return r, nil
	case <-ctx.Done():
		p.forget(l, id)
		return r, fmt.Errorf("%w: %v", ErrUnknown, ctx.Err())
	}
}

func (p *Proxy) forget(l *link, id string) {
	p.mu.Lock()
	delete(l.pending, id)
	p.mu.Unlock()
}

// Handler accepts Codex TUI connections (codex --remote unix://...).
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser clients are not supported", http.StatusForbidden)
			return
		}
		up, err := dial(r.Context(), p.upstream)
		if err != nil {
			http.Error(w, "App Server unavailable", http.StatusServiceUnavailable)
			return
		}
		defer up.Close()
		tui, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer tui.Close()
		l := &link{up: up, threadCalls: map[string]bool{}, turnCalls: map[string]bool{}, pending: map[string]chan reply{}}
		defer p.close(l)
		done := make(chan struct{})
		go func() { defer close(done); defer up.Close(); p.fromTUI(l, tui) }()
		p.fromServer(l, tui)
		tui.Close()
		<-done
	})
}

func (p *Proxy) close(l *link) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l.closed = true
	if p.bound == l {
		p.bound = nil
	}
	for id, ch := range l.pending {
		ch <- reply{lost: true}
		delete(l.pending, id)
	}
}

func dial(ctx context.Context, socket string) (*websocket.Conn, error) {
	d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	c, resp, err := d.DialContext(ctx, "ws://localhost/", nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return c, err
}

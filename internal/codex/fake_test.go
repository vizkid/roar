package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeServer imitates the App Server methods the proxy depends on.
type fakeServer struct {
	t       *testing.T
	sock    string
	mu      sync.Mutex
	threads int
	turn    string // active turn on any thread
	conns   []*websocket.Conn
	calls   []string // methods received, in order
	hold    chan struct{}
	holdFor string // method whose response waits on hold
}

func newFakeServer(t *testing.T) *fakeServer {
	dir, _ := os.MkdirTemp("/tmp", "roar-fake-")
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &fakeServer{t: t, sock: filepath.Join(dir, "server.sock"), hold: make(chan struct{})}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.serve)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return s
}

func (s *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	var wmu sync.Mutex
	send := func(v any) { wmu.Lock(); c.WriteJSON(v); wmu.Unlock() }
	for {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ThreadID       string `json:"threadId"`
				ExpectedTurnID string `json:"expectedTurnId"`
			} `json:"params"`
		}
		if c.ReadJSON(&m) != nil {
			return
		}
		s.mu.Lock()
		s.calls = append(s.calls, m.Method)
		hold := s.holdFor == m.Method
		s.mu.Unlock()
		if hold {
			go func() { <-s.hold; s.respond(m.ID, m.Method, m.Params.ThreadID, m.Params.ExpectedTurnID, send) }()
			continue
		}
		s.respond(m.ID, m.Method, m.Params.ThreadID, m.Params.ExpectedTurnID, send)
	}
}

func (s *fakeServer) respond(id json.RawMessage, method, thread, expected string, send func(any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := func(result any) { send(map[string]any{"id": id, "result": result}) }
	switch method {
	case "initialize":
		reply(map[string]any{"userAgent": "fake"})
	case "thread/start", "thread/resume", "thread/fork":
		s.threads++
		reply(map[string]any{"thread": map[string]any{"id": fmt.Sprintf("T%d", s.threads), "ephemeral": false, "source": "cli", "turns": []any{}}})
	case "turn/start":
		s.turn = fmt.Sprintf("U%d", len(s.calls))
		reply(map[string]any{"turn": map[string]any{"id": s.turn, "status": "inProgress"}})
		send(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": s.turn}}})
	case "turn/steer":
		if expected != s.turn || s.turn == "" {
			send(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "expected active turn id `" + expected + "` but found `" + s.turn + "`"}})
			return
		}
		reply(map[string]any{"turnId": s.turn})
	default:
		if len(id) > 0 {
			reply(map[string]any{})
		}
	}
}

// complete ends the active turn on every connection.
func (s *fakeServer) complete(thread string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.WriteJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": s.turn}}})
	}
	s.turn = ""
}

func (s *fakeServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

func (s *fakeServer) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c == method {
			n++
		}
	}
	return n
}

// fakeTUI is a Codex TUI connected to the proxy.
type fakeTUI struct {
	t    *testing.T
	c    *websocket.Conn
	seq  int
	got  chan map[string]any
	wmu  sync.Mutex
	name string
}

func connectTUI(t *testing.T, proxySock, name string) *fakeTUI {
	t.Helper()
	d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", proxySock)
	}}
	c, _, err := d.Dial("ws://localhost/", nil)
	if err != nil {
		t.Fatal(err)
	}
	u := &fakeTUI{t: t, c: c, got: make(chan map[string]any, 64), name: name}
	go func() {
		for {
			var m map[string]any
			if c.ReadJSON(&m) != nil {
				close(u.got)
				return
			}
			u.got <- m
		}
	}()
	t.Cleanup(func() { c.Close() })
	u.call("initialize", map[string]any{"clientInfo": map[string]any{"name": name}})
	u.notify("initialized")
	return u
}

func (u *fakeTUI) call(method string, params any) map[string]any {
	u.t.Helper()
	u.seq++
	id := u.seq
	u.wmu.Lock()
	u.c.WriteJSON(map[string]any{"id": id, "method": method, "params": params})
	u.wmu.Unlock()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case m, ok := <-u.got:
			if !ok {
				return nil
			}
			if v, ok := m["id"].(float64); ok && int(v) == id {
				return m
			}
		case <-timeout:
			u.t.Fatalf("%s: no reply to %s", u.name, method)
		}
	}
}

func (u *fakeTUI) notify(method string) {
	u.wmu.Lock()
	u.c.WriteJSON(map[string]any{"method": method})
	u.wmu.Unlock()
}

// sawHidden reports whether the TUI ever received a frame for a proxy call.
func (u *fakeTUI) drain() []map[string]any {
	var out []map[string]any
	for {
		select {
		case m := <-u.got:
			out = append(out, m)
		default:
			return out
		}
	}
}

package codex

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// fakeDaemon imitates the Codex App Server daemon methods delivery uses.
// It listens where Socket(home) says, in a short /tmp directory, because
// macOS limits socket paths to 104 bytes.
type fakeDaemon struct {
	home    string
	mu      sync.Mutex
	status  map[string]string // thread -> idle, active, notLoaded
	turn    string            // the in-progress turn on any active thread
	calls   []string
	conns   []*websocket.Conn
	hold    chan struct{}
	holdFor string // method whose response waits on hold
	endTurn bool   // the next steer finds the turn already over
	reject  string // method answered with an RPC error
	inputs  []string
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	home, err := os.MkdirTemp("/tmp", "rc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	d := &fakeDaemon{home: home, status: map[string]string{}, hold: make(chan struct{})}
	os.MkdirAll(home+"/app-server-control", 0o700)
	ln, err := net.Listen("unix", Socket(home))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(d.serve)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return d
}

func (d *fakeDaemon) set(thread, status, turn string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status[thread], d.turn = status, turn
}

func (d *fakeDaemon) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func (d *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	d.mu.Lock()
	d.conns = append(d.conns, c)
	d.mu.Unlock()
	var wmu sync.Mutex
	send := func(v any) { wmu.Lock(); c.WriteJSON(v); wmu.Unlock() }
	for {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ThreadID       string `json:"threadId"`
				ExpectedTurnID string `json:"expectedTurnId"`
				Input          []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if c.ReadJSON(&m) != nil {
			return
		}
		if len(m.ID) == 0 {
			continue // a notification such as initialized
		}
		d.mu.Lock()
		d.calls = append(d.calls, m.Method)
		hold := d.holdFor == m.Method
		d.mu.Unlock()
		respond := func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			reply := func(result any) { send(map[string]any{"id": m.ID, "result": result}) }
			fail := func(code int, msg string) {
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": code, "message": msg}})
			}
			if d.reject == m.Method {
				fail(-32000, "refused by test")
				return
			}
			thread := m.Params.ThreadID
			switch m.Method {
			case "initialize":
				reply(map[string]any{"userAgent": "fake"})
			case "thread/read":
				st, ok := d.status[thread]
				if !ok {
					fail(-32600, "thread not found: "+thread)
					return
				}
				reply(map[string]any{"thread": map[string]any{"id": thread, "status": map[string]any{"type": st}}})
			case "thread/turns/list":
				// Every input so far, newest first, as one user message per turn.
				var items []any
				for i := len(d.inputs) - 1; i >= 0; i-- {
					items = append(items, map[string]any{"type": "userMessage",
						"content": []any{map[string]any{"type": "text", "text": d.inputs[i]}}})
				}
				turns := []any{map[string]any{"id": "old", "status": "completed", "items": items}}
				if d.turn != "" {
					turns = []any{map[string]any{"id": d.turn, "status": "inProgress"}, turns[0]}
				}
				reply(map[string]any{"data": turns})
			case "turn/start":
				d.inputs = append(d.inputs, m.Params.Input[0].Text)
				d.turn, d.status[thread] = fmt.Sprintf("U%d", len(d.calls)), "active"
				reply(map[string]any{"turn": map[string]any{"id": d.turn, "status": "inProgress"}})
			case "turn/steer":
				if d.endTurn {
					d.endTurn, d.turn, d.status[thread] = false, "", "idle"
				}
				if m.Params.ExpectedTurnID != d.turn || d.turn == "" {
					fail(-32600, "no active turn to steer")
					return
				}
				d.inputs = append(d.inputs, m.Params.Input[0].Text)
				reply(map[string]any{"turnId": d.turn})
			default:
				reply(map[string]any{})
			}
		}
		if hold {
			go func() { <-d.hold; respond() }()
			continue
		}
		respond()
	}
}

func (d *fakeDaemon) dropAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		c.Close()
	}
}

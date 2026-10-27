package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gorilla/websocket"
)

// fromTUI forwards the TUI's frames to the App Server, noting the calls
// that change which thread the TUI shows.
func (p *Proxy) fromTUI(l *link, tui *websocket.Conn) {
	for {
		kind, data, err := tui.ReadMessage()
		if err != nil {
			return
		}
		var m wire
		if json.Unmarshal(data, &m) == nil {
			var id string
			if json.Unmarshal(m.ID, &id) == nil && strings.HasPrefix(id, idPrefix) {
				return // a forged proxy request: drop the connection
			}
			p.mu.Lock()
			switch {
			case m.Method == "initialized":
				l.initialized = true
			case len(m.ID) == 0:
			case m.Method == "thread/start" || m.Method == "thread/resume" || m.Method == "thread/fork":
				l.threadCalls[string(m.ID)] = true
			case m.Method == "turn/start":
				var tp struct {
					ThreadID string `json:"threadId"`
				}
				json.Unmarshal(m.Params, &tp)
				l.turnCalls[string(m.ID)] = tp.ThreadID
			}
			p.mu.Unlock()
		}
		l.write.Lock()
		err = l.up.WriteMessage(kind, data)
		l.write.Unlock()
		if err != nil {
			return
		}
	}
}

// fromServer forwards the App Server's frames to the TUI, except answers to
// the proxy's own calls, and tracks the bound thread and its active turn.
func (p *Proxy) fromServer(l *link, tui *websocket.Conn) {
	for {
		kind, data, err := l.up.ReadMessage()
		if err != nil {
			return
		}
		var m wire
		if json.Unmarshal(data, &m) == nil && p.observe(l, m) {
			continue
		}
		if err := tui.WriteMessage(kind, data); err != nil {
			return
		}
	}
}

type threadResult struct {
	Thread struct {
		ID        string          `json:"id"`
		Ephemeral bool            `json:"ephemeral"`
		Source    json.RawMessage `json:"source"`
		Turns     []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turns"`
	} `json:"thread"`
}

// observe updates the link from one server frame. It returns true when the
// frame answers a proxy call and must not reach the TUI.
func (p *Proxy) observe(l *link, m wire) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(m.ID) > 0 && m.Method == "" {
		var id string
		if json.Unmarshal(m.ID, &id) == nil && strings.HasPrefix(id, idPrefix) {
			if ch := l.pending[id]; ch != nil {
				ch <- reply{msg: m}
				delete(l.pending, id)
			}
			return true
		}
		key := string(m.ID)
		if l.threadCalls[key] {
			delete(l.threadCalls, key)
			p.bindThread(l, m)
		}
		if thread, ok := l.turnCalls[key]; ok {
			delete(l.turnCalls, key)
			var res struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			// The turn/start result arrives before turn/started. A turn on a
			// side thread (Codex names threads this way) leaves l.turn alone.
			if thread == l.thread && m.Error == nil && json.Unmarshal(m.Result, &res) == nil && res.Turn.ID != "" {
				l.turn = res.Turn.ID
			}
		}
		return false
	}
	var ev struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	bad := json.Unmarshal(m.Params, &ev) != nil
	if p.Trace != nil && (strings.HasPrefix(m.Method, "turn/") || strings.HasPrefix(m.Method, "thread/")) {
		fmt.Fprintf(p.Trace, "%s thread=%s turn=%s bound=%s/%s\n", m.Method, ev.ThreadID, ev.Turn.ID, l.thread, l.turn)
	}
	if bad || ev.ThreadID == "" || ev.ThreadID != l.thread {
		return false
	}
	switch m.Method {
	case "turn/started":
		l.turn = ev.Turn.ID
	case "turn/completed":
		if l.turn == ev.Turn.ID {
			l.turn = ""
		}
	case "thread/closed":
		l.thread, l.turn = "", ""
	}
	return false
}

// bindThread applies a successful thread call. The first connection whose
// own call opens a durable, top-level thread becomes the bound connection;
// later calls on it move delivery to the new thread. Calls on any other
// connection (a picker) never move delivery.
func (p *Proxy) bindThread(l *link, m wire) {
	var res threadResult
	if m.Error != nil || json.Unmarshal(m.Result, &res) != nil || res.Thread.ID == "" || res.Thread.Ephemeral {
		return
	}
	if p.bound == nil && topLevel(res.Thread.Source) {
		p.bound = l
	}
	if p.bound != l {
		return
	}
	if !topLevel(res.Thread.Source) {
		l.thread, l.turn = "", "" // a sub-agent thread: deliver nowhere until the next top-level one
		return
	}
	l.thread, l.turn = res.Thread.ID, ""
	for _, t := range res.Thread.Turns {
		if t.Status == "inProgress" {
			l.turn = t.ID
		}
	}
}

func topLevel(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	switch s {
	case "cli", "vscode", "appServer", "exec":
		return true
	}
	return false
}

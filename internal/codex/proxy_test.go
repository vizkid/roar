package codex

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startProxy(t *testing.T, s *fakeServer) (*Proxy, string) {
	t.Helper()
	p := NewProxy(s.sock)
	sock := filepath.Join(filepath.Dir(s.sock), "proxy.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: p.Handler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); os.Remove(sock) })
	return p, sock
}

func deliver(t *testing.T, p *Proxy, text string) (Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.Deliver(ctx, text)
}

func TestNotReadyUntilTheTUIOpensAThread(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	if _, err := deliver(t, p, "x"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("before any TUI: %v", err)
	}
	tui := connectTUI(t, sock, "codex-tui")
	if _, err := deliver(t, p, "x"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("before a thread: %v", err)
	}
	tui.call("thread/start", map[string]any{})
	r, err := deliver(t, p, "hello")
	if err != nil || r.Method != "turn/start" || r.Thread != "T1" {
		t.Fatalf("receipt %+v err %v", r, err)
	}
}

func TestBusyThreadIsSteeredAndStaleSteerRetries(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	tui.call("turn/start", map[string]any{"threadId": "T1"}) // the user starts a turn
	time.Sleep(100 * time.Millisecond)
	r, err := deliver(t, p, "steer me")
	if err != nil || r.Method != "turn/steer" {
		t.Fatalf("receipt %+v err %v", r, err)
	}
	// The turn changes without the proxy hearing: the next steer is stale,
	// which proves no delivery, so the error must say it is safe to retry.
	s.mu.Lock()
	s.turn = "other"
	s.mu.Unlock()
	if _, err := deliver(t, p, "late"); !errors.Is(err, ErrRejected) {
		t.Fatalf("stale steer: %v", err)
	}
	s.complete("T1")
	time.Sleep(100 * time.Millisecond)
	if r, err := deliver(t, p, "after"); err != nil || r.Method != "turn/start" {
		t.Fatalf("after completion: %+v %v", r, err)
	}
}

// Codex starts turns on side threads (to name a thread, for one) over the
// TUI's own connection. Those turns must not mark the bound thread busy:
// live, the stale turn made every later message a rejected steer.
func TestSideThreadTurnDoesNotBusyTheBoundThread(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	tui.call("turn/start", map[string]any{"threadId": "side"})
	time.Sleep(100 * time.Millisecond)
	if r, err := deliver(t, p, "x"); err != nil || r.Method != "turn/start" {
		t.Fatalf("idle bound thread: %+v %v", r, err)
	}
}

func TestNewThreadRebinds(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	tui.call("thread/start", map[string]any{}) // the user runs /new
	if r, err := deliver(t, p, "x"); err != nil || r.Thread != "T2" {
		t.Fatalf("after /new: %+v %v", r, err)
	}
}

func TestPickerConnectionDoesNotRebind(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	main := connectTUI(t, sock, "codex-tui")
	main.call("thread/start", map[string]any{})
	picker := connectTUI(t, sock, "codex-tui")
	picker.call("thread/resume", map[string]any{"threadId": "old"})
	if r, err := deliver(t, p, "x"); err != nil || r.Thread != "T1" {
		t.Fatalf("picker moved delivery: %+v %v", r, err)
	}
}

func TestPendingThreadCallBlocksDelivery(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	s.mu.Lock()
	s.holdFor = "thread/start"
	s.mu.Unlock()
	go tui.call("thread/start", map[string]any{})
	time.Sleep(150 * time.Millisecond)
	if _, err := deliver(t, p, "x"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("delivered during a thread switch: %v", err)
	}
	close(s.hold)
	time.Sleep(150 * time.Millisecond)
	if r, err := deliver(t, p, "x"); err != nil || r.Thread != "T2" {
		t.Fatalf("after the switch: %+v %v", r, err)
	}
}

func TestProxyCallsAreHiddenFromTheTUI(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	tui.drain()
	deliver(t, p, "x")
	time.Sleep(100 * time.Millisecond)
	for _, m := range tui.drain() {
		if id, ok := m["id"].(string); ok && strings.HasPrefix(id, idPrefix) {
			t.Fatalf("TUI saw a proxy response: %v", m)
		}
	}
	// A TUI frame using the reserved prefix closes its connection.
	tui.c.WriteJSON(map[string]any{"id": idPrefix + "1", "method": "turn/start"})
	time.Sleep(200 * time.Millisecond)
	if s.count("turn/start") != 1 {
		t.Fatal("a forged proxy frame reached the server")
	}
}

func TestDisconnectAfterWriteIsUnknown(t *testing.T) {
	s := newFakeServer(t)
	p, sock := startProxy(t, s)
	tui := connectTUI(t, sock, "codex-tui")
	tui.call("thread/start", map[string]any{})
	s.mu.Lock()
	s.holdFor = "turn/start"
	s.mu.Unlock()
	go func() { time.Sleep(200 * time.Millisecond); s.dropAll() }()
	if _, err := deliver(t, p, "x"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("disconnect after write: %v", err)
	}
}

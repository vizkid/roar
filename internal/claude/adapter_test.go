package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/store"
)

// fakeClaude plays Claude's side of the MCP pipe and its transcript.
type fakeClaude struct {
	t          *testing.T
	st         *store.Store
	in         *io.PipeWriter
	notes      chan map[string]any
	replies    chan map[string]any
	transcript string
	mu         sync.Mutex
	status     string
	cancel     context.CancelFunc
	done       chan error
}

const block = "block:claude-pane"

func startAdapter(t *testing.T, st *store.Store, argv []string, writer string) *fakeClaude {
	t.Helper()
	f := &fakeClaude{t: t, st: st, notes: make(chan map[string]any, 16), replies: make(chan map[string]any, 16),
		transcript: filepath.Join(t.TempDir(), "t.jsonl"), status: "idle", done: make(chan error, 1)}
	os.WriteFile(f.transcript, nil, 0o600)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	f.in = inW
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	cfg := Config{
		Store: st, Session: "session:s", Block: block, ParentPID: 1,
		ParentArgs: func(int) ([]string, error) { return argv, nil },
		Transcript: func() (string, error) { return f.transcript, nil },
		AgentStatus: func(context.Context) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.status, nil
		},
		Now: time.Now, Writer: writer, Poll: 50 * time.Millisecond, Grace: 300 * time.Millisecond,
	}
	go func() { f.done <- Serve(ctx, cfg, inR, outW); outW.Close() }()
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			if _, ok := m["id"]; ok {
				f.replies <- m
			} else {
				f.notes <- m
			}
		}
	}()
	t.Cleanup(func() { inW.Close(); cancel(); <-f.done })
	return f
}

func (f *fakeClaude) send(v map[string]any) {
	b, _ := json.Marshal(v)
	f.in.Write(append(b, '\n'))
}

func (f *fakeClaude) initialize() map[string]any {
	f.send(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25"}})
	r := <-f.replies
	f.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return r
}

func (f *fakeClaude) setStatus(s string) {
	f.mu.Lock()
	f.status = s
	f.mu.Unlock()
}

func (f *fakeClaude) record(kind, id string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	text := envelopeText(id)
	var v map[string]any
	switch kind {
	case "enqueue", "remove":
		v = map[string]any{"type": "queue-operation", "operation": kind, "timestamp": now, "sessionId": "s", "content": text}
	case "user":
		v = map[string]any{"type": "user", "isMeta": true, "timestamp": now, "sessionId": "s",
			"origin": map[string]string{"kind": "channel", "server": "roar"}, "message": map[string]any{"content": text}}
	case "attachment":
		v = map[string]any{"type": "attachment", "timestamp": now, "sessionId": "s", "attachment": map[string]any{
			"type": "queued_command", "prompt": text, "isMeta": true, "origin": map[string]string{"kind": "channel", "server": "roar"}}}
	}
	b, _ := json.Marshal(v)
	fh, _ := os.OpenFile(f.transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	fh.Write(append(b, '\n'))
	fh.Close()
}

func (f *fakeClaude) note(within time.Duration) map[string]any {
	select {
	case n := <-f.notes:
		return n
	case <-time.After(within):
		return nil
	}
}

func put(t *testing.T, st *store.Store, kind envelope.Kind) string {
	t.Helper()
	e := envelope.Envelope{
		ID: envelope.NewID(time.Now()), Kind: kind, Text: "hi", CreatedAt: time.Now(),
		From: envelope.Address{HostID: "h", Session: "session:a", Block: "block:a", Label: "a"},
		To:   envelope.Address{HostID: "h", Session: "session:s", Block: block},
	}
	if _, err := st.Put(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func waitState(t *testing.T, st *store.Store, id string, want store.State) store.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := st.Get(id); m.Record.State == want {
			return m.Record
		}
		time.Sleep(20 * time.Millisecond)
	}
	m, _ := st.Get(id)
	t.Fatalf("%s stayed %s (%+v), want %s", id, m.Record.State, m.Record, want)
	return store.Record{}
}

func newStore(t *testing.T) *store.Store {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func noteID(n map[string]any) string {
	p, _ := n["params"].(map[string]any)
	m, _ := p["meta"].(map[string]any)
	id, _ := m["id"].(string)
	return id
}

func TestDeliverAcceptReceive(t *testing.T) {
	st := newStore(t)
	f := startAdapter(t, st, []string{"claude", ChannelArg}, "w1")
	r := f.initialize()
	caps, _ := json.Marshal(r["result"])
	if !strings.Contains(string(caps), `"claude/channel"`) {
		t.Fatalf("no channel capability: %s", caps)
	}
	if !st.AdapterLive(block) {
		t.Fatal("adapter lock not held")
	}
	id := put(t, st, envelope.Inform)
	n := f.note(3 * time.Second)
	if noteID(n) != id || !strings.Contains(n["params"].(map[string]any)["content"].(string), "[roar "+id) {
		t.Fatalf("notification %v", n)
	}
	waitState(t, st, id, store.Written)
	f.record("enqueue", id)
	waitState(t, st, id, store.Accepted)
	f.record("user", id)
	rec := waitState(t, st, id, store.Read)
	if rec.Proof != "claude_transcript" || rec.Boundary != "turn" {
		t.Fatalf("receipt %+v", rec)
	}
}

func TestMidTurnReceipt(t *testing.T) {
	st := newStore(t)
	f := startAdapter(t, st, []string{"claude", ChannelArg}, "w1")
	f.initialize()
	id := put(t, st, envelope.Inform)
	f.note(3 * time.Second)
	f.record("enqueue", id)
	f.record("remove", id)
	f.record("attachment", id)
	if rec := waitState(t, st, id, store.Read); rec.Boundary != "mid_turn" {
		t.Fatalf("boundary %q", rec.Boundary)
	}
}

func TestRequestWaitsForRest(t *testing.T) {
	st := newStore(t)
	f := startAdapter(t, st, []string{"claude", ChannelArg}, "w1")
	f.setStatus("working")
	f.initialize()
	id := put(t, st, envelope.Request)
	if n := f.note(500 * time.Millisecond); n != nil {
		t.Fatalf("request written while working: %v", n)
	}
	if m, _ := st.Get(id); m.Record.HoldReason != "busy" {
		t.Fatalf("hold reason %q", m.Record.HoldReason)
	}
	f.setStatus("done")
	if noteID(f.note(3*time.Second)) != id {
		t.Fatal("request not written once at rest")
	}
}

func TestRemovedWithoutReceiptIsRequeuedAndReplayed(t *testing.T) {
	st := newStore(t)
	f := startAdapter(t, st, []string{"claude", ChannelArg}, "w1")
	f.initialize()
	id := put(t, st, envelope.Inform)
	f.note(3 * time.Second)
	f.record("enqueue", id)
	f.record("remove", id)
	if noteID(f.note(3*time.Second)) != id {
		t.Fatal("not replayed after removal without receipt")
	}
}

func TestGuardRefusesWithoutFlag(t *testing.T) {
	st := newStore(t)
	f := startAdapter(t, st, []string{"claude"}, "w1")
	r := f.initialize()
	caps, _ := json.Marshal(r["result"])
	if strings.Contains(string(caps), "claude/channel") {
		t.Fatalf("channel declared without the flag: %s", caps)
	}
	if st.AdapterLive(block) {
		t.Fatal("adapter lock taken without a channel")
	}
	put(t, st, envelope.Inform)
	if n := f.note(400 * time.Millisecond); n != nil {
		t.Fatalf("notification without a channel: %v", n)
	}
}

func TestRecoveryAfterWriterChange(t *testing.T) {
	st := newStore(t)
	// The old adapter wrote one message Claude received, and one it never saw.
	got, lost := put(t, st, envelope.Inform), put(t, st, envelope.Inform)
	for _, id := range []string{got, lost} {
		c, _ := st.Claim(id)
		c.Move(store.Written, time.Now(), func(r *store.Record) { r.Writer, r.WrittenAt = "old", time.Now() })
		c.Release()
	}
	f := &fakeClaude{t: t}
	_ = f
	f = startAdapter(t, st, []string{"claude", ChannelArg}, "new")
	f.record("enqueue", got)
	f.record("user", got)
	f.initialize()
	waitState(t, st, got, store.Read)
	if noteID(f.note(3*time.Second)) != lost {
		t.Fatal("the unseen message was not replayed by the new writer")
	}
}

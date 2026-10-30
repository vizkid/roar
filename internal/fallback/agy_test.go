package fallback

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/agenttest"
	"github.com/vizkid/roar/internal/store"
)

// agyPane starts the test agent as agy with its fake agent API, and binds
// the pane the way `roar bind agy` would, with the given token. The test
// agent accepts token T1.
func (f *fixture) agyPane(token string, pid int) (*agenttest.Agent, string) {
	f.t.Helper()
	api := f.t.TempDir()
	a := agenttest.Start(f.t, f.rex, f.bin, "agy", "TESTAGENT_API="+api)
	p, err := f.rex.Process(f.t.Context(), a.Session, a.Block)
	if err != nil || p.Foreground == nil {
		f.t.Fatal(err)
	}
	if pid == 0 {
		pid = p.Foreground.PID
	}
	b := store.AgyBinding{Conversation: "C1", Address: api, Token: token, Exe: filepath.Join(f.bin, "agy"), Session: a.Session, PID: pid}
	if err := f.st.BindAgy(a.Block, b); err != nil {
		f.t.Fatal(err)
	}
	return a, api
}

// received waits for n prompts: the agent API hands messages over on its own time.
func received(a *agenttest.Agent, n int) []string {
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end) && len(a.Submitted()) < n; {
		time.Sleep(50 * time.Millisecond)
	}
	return a.Submitted()
}

func (f *fixture) record(id string) store.Record {
	m, _ := f.st.Get(id)
	return m.Record
}

func TestAgyIdleIsTurn(t *testing.T) {
	f := setup(t)
	a, _ := f.agyPane("T1", 0)
	e := f.queue(a.Session, a.Block, "first line\nsecond line")
	want(t, f.attempt(e.ID), store.Read, "")
	if got := received(a, 1); len(got) != 1 || got[0] != e.Render() {
		t.Fatalf("agy received %q, want %q", agenttest.Joined(got), e.Render())
	}
	if r := f.record(e.ID); r.Proof != "agy_send_message" || r.Boundary != "turn" {
		t.Fatalf("record %+v", r)
	}
}

// Typed input waits in agy's queue during a turn; the agent API adds it to
// the running turn.
func TestAgyBusyIsMidTurn(t *testing.T) {
	f := setup(t)
	a, _ := f.agyPane("T1", 0)
	a.Control("working")
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Read, "")
	if r := f.record(e.ID); r.Proof != "agy_send_message" || r.Boundary != "mid_turn" {
		t.Fatalf("record %+v", r)
	}
	if len(received(a, 1)) != 1 {
		t.Fatal("not delivered")
	}
}

// A message sent behind a permission prompt could be read as its answer.
func TestAgyModalHolds(t *testing.T) {
	f := setup(t)
	a, _ := f.agyPane("T1", 0)
	a.Control("blocked")
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Queued, "modal")
	if got := a.Submitted(); len(got) != 0 {
		t.Fatalf("delivered behind a prompt: %q", agenttest.Joined(got))
	}
}

// A rejected call sent nothing, so the same attempt may type. The stale
// binding goes: its token will not work again.
func TestAgyRejectedFallsBackToTyping(t *testing.T) {
	f := setup(t)
	a, _ := f.agyPane("stale", 0)
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Submitted, "")
	if r := f.record(e.ID); r.Proof != "rex_submit" {
		t.Fatalf("record %+v", r)
	}
	if _, ok := f.st.AgyBinding(a.Block); ok {
		t.Fatal("rejected binding kept")
	}
	if got := a.Submitted(); len(got) != 1 {
		t.Fatalf("agy received %q", agenttest.Joined(got))
	}
}

// A reply Roar cannot read may follow a delivery. Never send twice.
func TestAgyGarbledIsUnknown(t *testing.T) {
	f := setup(t)
	a, api := f.agyPane("T1", 0)
	if err := os.WriteFile(filepath.Join(api, "garble"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Unknown, "agy_reply_unreadable")
	if got := received(a, 1); len(got) != 1 {
		t.Fatalf("agy received %q", agenttest.Joined(got))
	}
}

// A binding from an agy that has exited holds a dead token. It is deleted,
// and the message is typed into whatever agy runs now.
func TestAgyDeadBindingDeleted(t *testing.T) {
	f := setup(t)
	a, _ := f.agyPane("T1", 1)
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Submitted, "")
	if _, ok := f.st.AgyBinding(a.Block); ok {
		t.Fatal("dead binding kept")
	}
}

// Without a binding, a working agy waits like Claude: typed input would
// only join its queue.
func TestAgyUnboundWaitsWhileBusy(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "agy")
	a.Control("working")
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Queued, "busy")
}

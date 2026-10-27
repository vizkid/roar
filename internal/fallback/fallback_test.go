package fallback

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/agenttest"
	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

type fixture struct {
	t   *testing.T
	rex rex.Client
	st  *store.Store
	d   *Deliverer
	bin string
}

func setup(t *testing.T) *fixture {
	c := rextest.Require(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, rex: c, st: st, d: &Deliverer{Store: st, Rex: c, Now: time.Now}, bin: agenttest.Build(t)}
}

// queue stores a message addressed to the given pane.
func (f *fixture) queue(session, block, text string) envelope.Envelope {
	f.t.Helper()
	e := envelope.Envelope{
		ID:        envelope.NewID(time.Now()),
		From:      envelope.Address{HostID: "h", Session: "session:sender", Block: "block:sender", Label: "sender"},
		To:        envelope.Address{HostID: "h", Session: session, Block: block},
		Kind:      envelope.Inform,
		Text:      text,
		CreatedAt: time.Now(),
	}
	if _, err := f.st.Put(e, time.Now()); err != nil {
		f.t.Fatal(err)
	}
	return e
}

func (f *fixture) attempt(id string) Outcome {
	f.t.Helper()
	out, err := f.d.Attempt(f.t.Context(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func want(t *testing.T, got Outcome, state store.State, reason string) {
	t.Helper()
	if got.State != state || got.Reason != reason {
		t.Fatalf("outcome %s/%s, want %s/%s", got.State, got.Reason, state, reason)
	}
}

func TestSubmitsIntoEmptyComposer(t *testing.T) {
	f := setup(t)
	for _, kind := range []string{"claude", "codex"} {
		a := agenttest.Start(t, f.rex, f.bin, kind)
		e := f.queue(a.Session, a.Block, "first line\nsecond line")
		want(t, f.attempt(e.ID), store.Submitted, "")
		if got := a.Submitted(); len(got) != 1 || got[0] != e.Render() {
			t.Fatalf("%s received %q, want %q", kind, agenttest.Joined(got), e.Render())
		}
		m, _ := f.st.Get(e.ID)
		if m.Record.ForegroundPID == 0 || m.Record.Proof != "rex_submit" {
			t.Fatalf("record %+v", m.Record)
		}
	}
}

func TestHoldsOnDraftThenDelivers(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "claude")
	a.Type(f.rex, "my own words")
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Queued, "draft")
	if m, _ := f.st.Get(e.ID); m.Record.HoldReason != "draft" {
		t.Fatalf("hold reason %q", m.Record.HoldReason)
	}
	a.Control("clear")
	want(t, f.attempt(e.ID), store.Submitted, "")
	if got := a.Submitted(); len(got) != 1 || strings.Contains(got[0], "my own words") {
		t.Fatalf("received %q", agenttest.Joined(got))
	}
}

func TestHoldsWhileBusyOrModal(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "codex")
	e := f.queue(a.Session, a.Block, "hello")
	a.Control("working")
	want(t, f.attempt(e.ID), store.Queued, "busy")
	a.Control("blocked")
	want(t, f.attempt(e.ID), store.Queued, "modal")
	if got := a.Submitted(); len(got) != 0 {
		t.Fatalf("typed while not ready: %q", agenttest.Joined(got))
	}
}

func TestNeverTypesIntoShellAndExpiresDeadPane(t *testing.T) {
	f := setup(t)
	session, shell := rextest.Session(t, f.rex, rextest.Label("shell"), "sh")
	e := f.queue(session, shell, "rm -rf nothing")
	want(t, f.attempt(e.ID), store.Queued, "wrong_program")
	screen, _ := f.rex.Run(t.Context(), "capture", "-s", session, "-b", shell)
	if strings.Contains(string(screen), "rm -rf") {
		t.Fatal("text reached the shell")
	}

	session, dead := rextest.Session(t, f.rex, rextest.Label("dead"), "true")
	time.Sleep(500 * time.Millisecond)
	e = f.queue(session, dead, "hello")
	want(t, f.attempt(e.ID), store.Expired, "dead_pane")
}

func TestChangeBetweenPasteAndEnterLeavesUnknown(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "claude", "TESTAGENT_ON_PASTE=working")
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Unknown, "changed_before_enter")
	if got := a.Submitted(); len(got) != 0 {
		t.Fatalf("pressed Enter anyway: %q", agenttest.Joined(got))
	}
	// Unknown is settled only by an operator; another attempt does nothing.
	want(t, f.attempt(e.ID), store.Unknown, "changed_before_enter")
}

func TestRacingDeliverersSubmitOnce(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "claude")
	e := f.queue(a.Session, a.Block, "once")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &Deliverer{Store: f.st, Rex: f.rex, Now: time.Now}
			d.Attempt(context.Background(), e.ID)
		}()
	}
	wg.Wait()
	time.Sleep(500 * time.Millisecond)
	if got := a.Submitted(); len(got) != 1 {
		t.Fatalf("submitted %d times", len(got))
	}
}

func TestSkipsPaneWithNativeAdapter(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "claude")
	lock, err := f.st.HoldAdapter(a.Block)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Queued, "native_adapter")
	if len(a.Submitted()) != 0 {
		t.Fatal("fallback typed into a pane with a native adapter")
	}
}

func TestWatchDeliversWhenAgentComesToRest(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "claude")
	a.Control("working")
	e := f.queue(a.Session, a.Block, "later")
	want(t, f.attempt(e.ID), store.Queued, "busy")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go f.d.Watch(ctx)
	time.Sleep(time.Second) // let the event stream start
	a.Control("done")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := f.st.Get(e.ID); m.Record.State == store.Submitted {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	m, _ := f.st.Get(e.ID)
	t.Fatalf("watch did not deliver: %+v", m.Record)
}

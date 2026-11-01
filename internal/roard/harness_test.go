package roard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

const testHost = "host:roard-test"

// fakeDelivery records which panes roard asked it to deliver to; fn may
// change the store as a real delivery would.
type fakeDelivery struct {
	mu     sync.Mutex
	blocks []string
	fn     func(block string)
}

func (f *fakeDelivery) Block(_ context.Context, block string) {
	f.mu.Lock()
	f.blocks = append(f.blocks, block)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		fn(block)
	}
}

func (f *fakeDelivery) count(block string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.blocks {
		if b == block {
			n++
		}
	}
	return n
}

type harness struct {
	t     *testing.T
	st    *store.Store
	deps  Deps
	del   *fakeDelivery
	label string
	ep    string
}

func newHarness(t *testing.T, set string) *harness {
	rextest.Require(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := config.Load(config.Options{Path: t.TempDir() + "/none.toml", Set: set})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	h := &harness{t: t, st: st, del: &fakeDelivery{}, label: rextest.Label("roard"), ep: rexctl.LocalEndpoint(os.Getenv, home)}
	h.deps = Deps{Store: st, Settings: func() *config.Settings { return s }, Endpoint: h.ep, HostID: testHost,
		Version: "test", Label: h.label, Delivery: h.del, UID: os.Getuid(), Logf: t.Logf}
	return h
}

// start runs roard until the test ends and waits for its label.
func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.deps) }()
	h.t.Cleanup(func() { cancel(); <-done })
	c := h.sender(nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := c.Resolve(h.t.Context(), h.label); err == nil {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("roard never declared its label")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sender is a Rex client that offers roar.result and collects calls to it.
func (h *harness) sender(results chan<- Result) *rexctl.Client {
	if results == nil {
		return h.senderWith(nil)
	}
	return h.senderWith(func(p rexctl.Perform) (any, *rexctl.Error) {
		var r Result
		json.Unmarshal(p.Args, &r)
		results <- r
		return "ok", nil
	})
}

// senderWith answers roar.result with handle; nil offers no action.
func (h *harness) senderWith(handle func(rexctl.Perform) (any, *rexctl.Error)) *rexctl.Client {
	h.t.Helper()
	o := rexctl.Options{Endpoint: h.ep, Info: rexctl.Info{Kind: "cli", Name: "roar-test"}, OnPerform: handle}
	ctx, cancel := context.WithTimeout(h.t.Context(), 10*time.Second)
	defer cancel()
	c, err := rexctl.Dial(ctx, o)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(c.Close)
	if handle != nil {
		if err := c.Declare(ctx, rextest.Label("sender"), []rexctl.Action{{Name: "roar.result"}}); err != nil {
			h.t.Fatal(err)
		}
	}
	return c
}

func (h *harness) call(c *rexctl.Client, action string, args, out any) error {
	id, err := c.Resolve(h.t.Context(), h.label)
	if err != nil {
		return err
	}
	return c.Perform(h.t.Context(), id, action, args, 10*time.Second, out)
}

func (h *harness) envelope(text string) envelope.Envelope {
	return envelope.Envelope{ID: envelope.NewID(time.Now()), Kind: envelope.Inform, Text: text, CreatedAt: time.Now(),
		From: envelope.Address{HostID: "host:other", Session: "session:s", Block: "block:sender", Label: "sender"},
		To:   envelope.Address{HostID: testHost, Session: "session:r", Block: "block:r", Label: "r"}}
}

func enqueueArgs(e envelope.Envelope) map[string]any {
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return map[string]any{"envelope": string(b), "sha256": hex.EncodeToString(sum[:])}
}

// next waits for a result matching ok.
func next(t *testing.T, results <-chan Result, within time.Duration, ok func(Result) bool) Result {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case r := <-results:
			if ok(r) {
				return r
			}
		case <-deadline:
			t.Fatalf("no matching roar.result within %v", within)
		}
	}
}

func rowState(r Result, id, state string) bool {
	return slices.ContainsFunc(r.Rows, func(row Row) bool { return row.ID == id && row.State == state })
}

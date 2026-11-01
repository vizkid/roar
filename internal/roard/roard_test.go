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
	h.t.Helper()
	o := rexctl.Options{Endpoint: h.ep, Info: rexctl.Info{Kind: "cli", Name: "roar-test"}}
	if results != nil {
		o.OnPerform = func(p rexctl.Perform) (any, *rexctl.Error) {
			var r Result
			json.Unmarshal(p.Args, &r)
			results <- r
			return "ok", nil
		}
	}
	ctx, cancel := context.WithTimeout(h.t.Context(), 10*time.Second)
	defer cancel()
	c, err := rexctl.Dial(ctx, o)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(c.Close)
	if results != nil {
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

// 42, 43: one roard per store; a second exits at once with no error.
func TestOneRoardPerStore(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := Run(ctx, h.deps); err != nil || ctx.Err() != nil {
		t.Fatalf("second roard: %v (ctx %v)", err, ctx.Err())
	}
	var hello struct {
		Version  string
		HostID   string `json:"host_id"`
		Protocol int
		MaxPart  int `json:"max_part"`
	}
	if err := h.call(h.sender(nil), "roar.hello", nil, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.HostID != testHost || hello.Protocol != 1 || hello.MaxPart < 64<<10 {
		t.Fatalf("hello %+v", hello)
	}
}

// 46, 50: subscribe first, enqueue, and results arrive as calls, not polls.
func TestEnqueueCallsBackEachChange(t *testing.T) {
	h := newHarness(t, "")
	h.del.fn = func(block string) {
		msgs, _ := h.st.Inbox(block)
		for _, m := range msgs {
			c, err := h.st.Claim(m.Envelope.ID)
			if err == nil {
				c.Move(store.Read, time.Now(), func(r *store.Record) { r.Proof = "test" })
				c.Release()
			}
		}
	}
	h.start()
	results := make(chan Result, 16)
	s := h.sender(results)
	e := h.envelope("hello")
	var sub struct {
		Subscription string
		Rows         []Row
	}
	if err := h.call(s, "roar.subscribe", map[string]any{"ids": []string{e.ID}}, &sub); err != nil || sub.Subscription == "" {
		t.Fatalf("subscribe %+v: %v", sub, err)
	}
	var job struct{ Job string }
	if err := h.call(s, "roar.enqueue", enqueueArgs(e), &job); err != nil || job.Job == "" {
		t.Fatalf("enqueue %+v: %v", job, err)
	}
	r := next(t, results, 10*time.Second, func(r Result) bool { return rowState(r, e.ID, "read") })
	if r.Subscription != sub.Subscription {
		t.Fatalf("read arrived on %+v", r)
	}
	if h.del.count("block:r") == 0 {
		t.Fatal("no delivery attempt")
	}
}

// 49: a repeated enqueue returns the stored record and is attempted again.
func TestDuplicateEnqueueIsCovered(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	results := make(chan Result, 16)
	s := h.sender(results)
	e := h.envelope("twice")
	waitAttempts := func(n int) {
		deadline := time.Now().Add(5 * time.Second)
		for h.del.count("block:r") < n {
			if time.Now().After(deadline) {
				t.Fatalf("attempts %d, want %d", h.del.count("block:r"), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for i := range 2 {
		var job struct{ Job string }
		if err := h.call(s, "roar.enqueue", enqueueArgs(e), &job); err != nil {
			t.Fatal(err)
		}
		next(t, results, 10*time.Second, func(r Result) bool { return r.Job == job.Job && rowState(r, e.ID, "queued") })
		// A duplicate inside the coalesce window joins the attempt already
		// scheduled; one after the first attempt must cause another.
		waitAttempts(i + 1)
	}
}

func TestEnqueueRefusals(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	results := make(chan Result, 16)
	s := h.sender(results)

	bad := enqueueArgs(h.envelope("x"))
	bad["sha256"] = "00"
	if err := h.call(s, "roar.enqueue", bad, nil); rexctl.Code(err) != "refused" {
		t.Fatalf("bad digest: %v", err)
	}
	other := h.envelope("elsewhere")
	other.To.HostID = "host:someone-else"
	var job struct{ Job string }
	if err := h.call(s, "roar.enqueue", enqueueArgs(other), &job); err != nil {
		t.Fatal(err)
	}
	r := next(t, results, 10*time.Second, func(r Result) bool { return r.Job == job.Job })
	if r.Error == nil || r.Error.Code != "refused" {
		t.Fatalf("wrong host result %+v", r)
	}
}

// 53: a message stored by any process is attempted with no timer.
func TestStoredMessageWakesWatcher(t *testing.T) {
	h := newHarness(t, "watcher.gap_recheck=1h")
	h.start()
	e := h.envelope("local hold")
	if _, err := h.st.Put(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.del.count("block:r") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no attempt after a store change")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStatusJob(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	e := h.envelope("status me")
	h.st.Put(e, time.Now())
	results := make(chan Result, 16)
	s := h.sender(results)
	var job struct{ Job string }
	if err := h.call(s, "roar.status", map[string]any{"ids": []string{e.ID, "rm-01ZZZZZZZZZZZZZZZZZZZZZZZZ"}}, &job); err != nil {
		t.Fatal(err)
	}
	r := next(t, results, 10*time.Second, func(r Result) bool { return r.Job == job.Job })
	if len(r.Rows) != 2 || r.Rows[0].State != "queued" || r.Rows[1].State != "missing" {
		t.Fatalf("rows %+v", r.Rows)
	}
}

package roard

import (
	"context"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/store"
)

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

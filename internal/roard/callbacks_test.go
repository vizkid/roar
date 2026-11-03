package roard

import (
	"context"
	"testing"
	"time"
)

// waitDone fails unless the sender's consumer has ended within a second.
func waitDone(t *testing.T, sq *senderQueue) {
	t.Helper()
	select {
	case <-sq.done:
	case <-time.After(time.Second):
		t.Fatal("the sender's consumer is still running")
	}
}

func (c *callbacks) queueOf(client string) *senderQueue {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.q[client]
}

// Review, 2026-11-03: a sender's queue and goroutine lived until roard
// restarted, because only a failed callback removed them. With no Rex
// session here, each result is taken off the queue at once.
func TestCallbackQueueEndsWhenSenderLeaves(t *testing.T) {
	h := newHarness(t, "roard.callback.idle=1h")
	c := newCallbacks(context.Background(), h.deps)
	c.send("client:a", Result{Job: "j1"}, Hints{})
	sq := c.queueOf("client:a")
	if sq == nil {
		t.Fatal("no queue")
	}
	newSubs(c).drop("client:a") // client_left
	waitDone(t, sq)
	if c.queueOf("client:a") != nil {
		t.Fatal("queue kept after the sender left")
	}
}

// A client_left that came while roard was disconnected is never seen, so an
// idle queue ends on its own.
func TestCallbackQueueEndsWhenIdle(t *testing.T) {
	h := newHarness(t, "roard.callback.idle=20ms")
	c := newCallbacks(context.Background(), h.deps)
	c.send("client:a", Result{Job: "j1"}, Hints{})
	sq := c.queueOf("client:a")
	waitDone(t, sq)
	if c.queueOf("client:a") != nil {
		t.Fatal("idle queue kept")
	}
	c.send("client:a", Result{Job: "j2"}, Hints{}) // a new queue starts
	if c.queueOf("client:a") == nil {
		t.Fatal("no queue for a later result")
	}
}

// Queued subscription updates for one message keep only the newest, at the
// back so revisions stay in order. A full queue drops its oldest.
func TestSenderQueueMergesAndBounds(t *testing.T) {
	sq := &senderQueue{queue: newQueue[Result]()}
	row := func(id string) []Row { return []Row{{ID: id}} }
	sq.add(Result{Subscription: "s", Revision: 1, Rows: row("a")}, 3)
	sq.add(Result{Subscription: "s", Revision: 2, Rows: row("b")}, 3)
	sq.add(Result{Subscription: "s", Revision: 3, Rows: row("a")}, 3)
	if got := revisions(sq); got != "2,3" {
		t.Fatalf("after merge: %s", got)
	}
	sq.add(Result{Job: "j1"}, 3)
	if dropped := sq.add(Result{Job: "j2"}, 3); !dropped {
		t.Fatal("a full queue took a fourth result")
	}
	if got := revisions(sq); got != "3,0,0" {
		t.Fatalf("after bound: %s", got)
	}
}

func revisions(sq *senderQueue) string {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	s := ""
	for i, r := range sq.items {
		if i > 0 {
			s += ","
		}
		s += string(rune('0' + r.Revision))
	}
	return s
}

package rexctl

import (
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
)

// state is the label's client's connection_state in client.list, or "" when
// the client has left.
func state(t *testing.T, c *Client, id string) string {
	t.Helper()
	var out struct {
		Clients []struct {
			ClientID        string `json:"client_id"`
			ConnectionState string `json:"connection_state"`
		} `json:"clients"`
	}
	if err := c.Call(t.Context(), "client.list", nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, cl := range out.Clients {
		if cl.ClientID == id {
			return cl.ConnectionState
		}
	}
	return ""
}

// attached declares label on a client that, like roard, attached to every
// session, so Rex keeps it as lost for 60 s when its connection drops.
func attached(t *testing.T, ep, label string) *Client {
	c := dial(t, ep, Options{})
	if err := c.Declare(t.Context(), label, []Action{{Name: "roar.test.noop"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(t.Context(), "session.attach_all", map[string]string{"event_scope": "all"}, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

// A restarted roard left its old client lost for 60 s, and senders failed
// with roard_ambiguous. Resolve skips lost clients.
func TestResolveSkipsLostClient(t *testing.T) {
	ep := local(t)
	label := rextest.Label("ctl")
	old := attached(t, ep, label)
	old.conn.Close() // a drop, not a Close: no close frame
	b := dial(t, ep, Options{})
	waitState(t, b, old.ID(), "lost")
	cur := attached(t, ep, label)
	id, err := b.Resolve(t.Context(), label)
	if err != nil || id != cur.ID() {
		t.Fatalf("resolve %q: %v (want %q)", id, err, cur.ID())
	}
}

// Close says goodbye, so Rex drops the client at once instead of keeping it
// as lost.
func TestCloseLeavesAtOnce(t *testing.T) {
	ep := local(t)
	c := attached(t, ep, rextest.Label("ctl"))
	b := dial(t, ep, Options{})
	c.Close()
	waitState(t, b, c.ID(), "")
}

func waitState(t *testing.T, c *Client, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for got := state(t, c, id); got != want; got = state(t, c, id) {
		if time.Now().After(deadline) {
			t.Fatalf("client %s state %q, want %q", id, got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

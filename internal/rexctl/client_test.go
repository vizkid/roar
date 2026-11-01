package rexctl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
)

func dial(t *testing.T, endpoint string, o Options) *Client {
	t.Helper()
	o.Endpoint = endpoint
	if o.Info.Name == "" {
		o.Info = Info{Kind: "other", Name: "roar-test", Version: "0"}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	c, err := Dial(ctx, o)
	if err != nil {
		t.Fatalf("dial %s: %v", endpoint, err)
	}
	t.Cleanup(c.Close)
	return c
}

func local(t *testing.T) string {
	rextest.Require(t)
	home, _ := os.UserHomeDir()
	return LocalEndpoint(os.Getenv, home)
}

func TestDialLocal(t *testing.T) {
	c := dial(t, local(t), Options{})
	if !strings.HasPrefix(c.ID(), "client:") || c.MaxMessage() < 64<<10 {
		t.Fatalf("id %q max %d", c.ID(), c.MaxMessage())
	}
	var st map[string]any
	if err := c.Call(t.Context(), "server.status", nil, &st); err != nil {
		t.Fatal(err)
	}
}

func TestCallErrorCarriesRexCode(t *testing.T) {
	c := dial(t, local(t), Options{})
	err := c.Call(t.Context(), "client.inspect", map[string]string{"client_id": "client:00000000-0000-7000-8000-000000000000"}, nil)
	var re *Error
	if !errors.As(err, &re) || re.Code != "not_found" {
		t.Fatalf("err %v", err)
	}
}

// echo declares roar.test.echo on a fresh client and records each perform.
func echo(t *testing.T, endpoint string) (*Client, string, *[]Perform) {
	var mu sync.Mutex
	var seen []Perform
	a := dial(t, endpoint, Options{OnPerform: func(p Perform) (any, *Error) {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
		var args map[string]any
		json.Unmarshal(p.Args, &args)
		return map[string]any{"echo": args["text"], "len": len(p.Args)}, nil
	}})
	label := rextest.Label("ctl")
	if err := a.Declare(t.Context(), label, []Action{{Name: "roar.test.echo", Title: "Echo"}}); err != nil {
		t.Fatal(err)
	}
	return a, label, &seen
}

func TestPerformRoundTrip(t *testing.T) {
	ep := local(t)
	a, label, seen := echo(t, ep)
	b := dial(t, ep, Options{})
	id, err := b.Resolve(t.Context(), label)
	if err != nil || id != a.ID() {
		t.Fatalf("resolve %q: %v (want %q)", id, err, a.ID())
	}
	var out struct{ Echo string }
	if err := b.Perform(t.Context(), id, "roar.test.echo", map[string]string{"text": "hi"}, 5*time.Second, &out); err != nil || out.Echo != "hi" {
		t.Fatalf("perform %+v: %v", out, err)
	}
	p := (*seen)[0]
	if p.Requester.ClientID != b.ID() || p.Requester.Principal.Kind != "peer" ||
		p.Requester.Principal.UID == nil || *p.Requester.Principal.UID != os.Getuid() {
		t.Fatalf("requester %+v", p.Requester)
	}
}

func TestUndeclaredActionIsRefusedByRex(t *testing.T) {
	ep := local(t)
	a, _, seen := echo(t, ep)
	b := dial(t, ep, Options{})
	err := b.Perform(t.Context(), a.ID(), "roar.test.nope", nil, 5*time.Second, nil)
	var re *Error
	if !errors.As(err, &re) || re.Code != "not_supported" || len(*seen) != 0 {
		t.Fatalf("err %v seen %d", err, len(*seen))
	}
}

func TestResolveMissingLabel(t *testing.T) {
	b := dial(t, local(t), Options{})
	if _, err := b.Resolve(t.Context(), rextest.Label("nobody")); !errors.Is(err, ErrNoClient) {
		t.Fatalf("err %v", err)
	}
}

// The rex CLI refuses requests over 64 KiB; Roar's own client may send up
// to the server's limit.
func TestLargeMessage(t *testing.T) {
	ep := local(t)
	a, _, _ := echo(t, ep)
	b := dial(t, ep, Options{})
	text := strings.Repeat("x", 700<<10)
	var out struct{ Len int }
	if err := b.Perform(t.Context(), a.ID(), "roar.test.echo", map[string]string{"text": text}, 10*time.Second, &out); err != nil || out.Len < len(text) {
		t.Fatalf("len %d: %v", out.Len, err)
	}
}

func TestEventsArrive(t *testing.T) {
	ep := local(t)
	got := make(chan Event, 64)
	c := dial(t, ep, Options{OnEvent: func(e Event) { got <- e }})
	if err := c.Call(t.Context(), "session.attach_all", map[string]string{"event_scope": "session"}, nil); err != nil {
		t.Fatal(err)
	}
	label := rextest.Label("ev")
	rextest.Session(t, rextest.Require(t), label, "sleep", "30")
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-got:
			if e.Type == "session_created" && strings.Contains(string(e.Payload), label) {
				return
			}
		case <-deadline:
			t.Fatal("no session_created event")
		}
	}
}

func TestDoneAfterClose(t *testing.T) {
	c := dial(t, local(t), Options{})
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed")
	}
	if err := c.Call(context.Background(), "server.status", nil, nil); err == nil {
		t.Fatal("call after close succeeded")
	}
}

// The gate for the sender design: Roar's own client reaches another host's
// Rex server over Tailscale, offers an action there, and calls it with a
// large message. ROAR_LIVE_HOST is that server's https endpoint.
func TestLiveRemoteControl(t *testing.T) {
	ep := os.Getenv("ROAR_LIVE_HOST")
	if os.Getenv("ROAR_LIVE") != "1" || ep == "" {
		t.Skip("set ROAR_LIVE=1 and ROAR_LIVE_HOST=https://<host>-rex.<tailnet>.ts.net")
	}
	a, label, seen := echo(t, ep)
	b := dial(t, ep, Options{})
	id, err := b.Resolve(t.Context(), label)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var out struct {
		Echo string
		Len  int
	}
	if err := b.Perform(t.Context(), id, "roar.test.echo", map[string]string{"text": "remote"}, 10*time.Second, &out); err != nil || out.Echo != "remote" {
		t.Fatalf("small perform %+v: %v", out, err)
	}
	t.Logf("small perform round trip %v", time.Since(start))
	text := strings.Repeat("y", 700<<10)
	start = time.Now()
	if err := b.Perform(t.Context(), id, "roar.test.echo", map[string]string{"text": text}, 30*time.Second, &out); err != nil || out.Len < len(text) {
		t.Fatalf("700 KiB perform: %v", err)
	}
	t.Logf("700 KiB perform round trip %v", time.Since(start))
	if p := (*seen)[0]; p.Requester.Principal.Kind != "tailscale" || a.ID() == "" {
		t.Fatalf("principal %+v", p.Requester.Principal)
	}
}

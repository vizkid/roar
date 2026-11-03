package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vizkid/rexkit/rexctl"
	"github.com/vizkid/rexkit/rextest"
	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/roard"
	"github.com/vizkid/roar/internal/store"
)

const peerHost = "host:peer-test"

type markRead struct{ st *store.Store }

func (d markRead) Block(_ context.Context, block string) {
	msgs, _ := d.st.Inbox(block)
	for _, m := range msgs {
		if c, err := d.st.Claim(m.Envelope.ID); err == nil {
			c.Move(store.Read, time.Now(), func(r *store.Record) { r.Proof = "test" })
			c.Release()
		}
	}
}

type leaveQueued struct{}

func (leaveQueued) Block(context.Context, string) {}

type peerFixture struct {
	st    *store.Store
	label string
	ep    string
}

func settings(t *testing.T, set string) *config.Settings {
	s, _, err := config.Load(config.Options{Path: filepath.Join(t.TempDir(), "none.toml"), Set: set})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// startRoard runs a roard with its own label on the local Rex server.
func startRoard(t *testing.T, delivery func(*store.Store) roard.Delivery) peerFixture {
	rextest.Require(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	f := peerFixture{st: st, label: rextest.Label("roard"), ep: rexctl.LocalEndpoint(os.Getenv, home)}
	s := settings(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- roard.Run(ctx, roard.Deps{Store: st, Settings: func() *config.Settings { return s }, Endpoint: f.ep,
			HostID: peerHost, Version: "test", Label: f.label, Delivery: delivery(st), UID: os.Getuid()})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return f
}

func (f peerFixture) peer(t *testing.T, set string) *Peer {
	return &Peer{Endpoint: f.ep, Label: f.label, Settings: settings(t, set)}
}

func peerEnvelope(text string) envelope.Envelope {
	return envelope.Envelope{ID: envelope.NewID(time.Now()), Kind: envelope.Inform, Text: text, CreatedAt: time.Now(),
		From: envelope.Address{HostID: "host:other", Session: "session:s", Block: "block:sender", Label: "sender"},
		To:   envelope.Address{HostID: peerHost, Session: "session:r", Block: "block:r", Label: "r"}}
}

// waitRoard waits until the fixture's roard has declared its label.
func waitRoard(t *testing.T, p *Peer) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, err := p.Status(ctx, envelope.NewID(time.Now()))
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("roard not up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 46: a send settles by callback, with no status calls.
func TestPeerSendSettlesByCallback(t *testing.T) {
	f := startRoard(t, func(st *store.Store) roard.Delivery { return markRead{st} })
	p := f.peer(t, "")
	waitRoard(t, p)
	p.Stats = Stats{}
	r, err := p.Send(t.Context(), peerEnvelope("hi"), nil)
	if err != nil || r.State != "read" {
		t.Fatalf("send %+v: %v", r, err)
	}
	if p.Stats.Calls["roar.status"] != 0 || p.Stats.Calls["roar.enqueue"] != 1 {
		t.Fatalf("calls %v", p.Stats.Calls)
	}
}

// 47: 12 MiB arrives intact in at most 25 parts.
func TestPeerSendsLargeAttachmentInFewParts(t *testing.T) {
	f := startRoard(t, func(st *store.Store) roard.Delivery { return leaveQueued{} })
	p := f.peer(t, "sender.print_deadline=200ms")
	waitRoard(t, p)
	data := make([]byte, 12<<20)
	rand.Read(data)
	e := peerEnvelope("big")
	e.Files = []envelope.File{envelope.FileFor("big.bin", data)}
	start := time.Now()
	r, err := p.Send(t.Context(), e, map[string][]byte{"big.bin": data})
	if err != nil || r.State != "queued" {
		t.Fatalf("send %+v: %v", r, err)
	}
	t.Logf("12 MiB in %v, %d parts, %d calls", time.Since(start), p.Stats.Calls["roar.upload.part"], p.Stats.Total())
	if n := p.Stats.Calls["roar.upload.part"]; n == 0 || n > 25 {
		t.Fatalf("parts %d", n)
	}
	m, err := f.st.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(m.Dir, "files", "big.bin")); !bytes.Equal(got, data) {
		t.Fatal("stored file differs")
	}
}

// 48: a connection lost mid-upload resumes; no part is sent twice.
func TestPeerResumesAfterDrop(t *testing.T) {
	f := startRoard(t, func(st *store.Store) roard.Delivery { return leaveQueued{} })
	p := f.peer(t, "sender.print_deadline=200ms,sender.upload.part_size=256KiB,sender.connect.initial=10ms")
	waitRoard(t, p)
	data := make([]byte, 2<<20)
	rand.Read(data)
	e := peerEnvelope("resume")
	e.Files = []envelope.File{envelope.FileFor("f.bin", data)}
	dropped := false
	p.afterPart = func(c *rexctl.Client, n int) {
		if n == 3 && !dropped {
			dropped = true
			c.Close()
		}
	}
	if _, err := p.Send(t.Context(), e, map[string][]byte{"f.bin": data}); err != nil {
		t.Fatal(err)
	}
	if !dropped || p.Stats.PartBytes != int64(len(data)) {
		t.Fatalf("dropped %v, part bytes %d for %d", dropped, p.Stats.PartBytes, len(data))
	}
	if _, err := f.st.Get(e.ID); err != nil {
		t.Fatal(err)
	}
}

// 55: no roard there means a known refusal.
func TestPeerWithoutRoard(t *testing.T) {
	rextest.Require(t)
	home, _ := os.UserHomeDir()
	p := &Peer{Endpoint: rexctl.LocalEndpoint(os.Getenv, home), Label: rextest.Label("nobody"), Settings: settings(t, "")}
	_, err := p.Send(t.Context(), peerEnvelope("x"), nil)
	if !errors.Is(err, ErrNoRoard) {
		t.Fatalf("err %v", err)
	}
}

// The print deadline returns the latest state; it is not a failure.
func TestPeerPrintDeadline(t *testing.T) {
	f := startRoard(t, func(st *store.Store) roard.Delivery { return leaveQueued{} })
	p := f.peer(t, "sender.print_deadline=300ms")
	waitRoard(t, p)
	start := time.Now()
	r, err := p.Send(t.Context(), peerEnvelope("wait"), nil)
	if err != nil || r.State != "queued" || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("send %+v after %v: %v", r, time.Since(start), err)
	}
}

func TestPeerStatus(t *testing.T) {
	f := startRoard(t, func(st *store.Store) roard.Delivery { return leaveQueued{} })
	p := f.peer(t, "")
	waitRoard(t, p)
	e := peerEnvelope("status")
	f.st.Put(e, time.Now())
	rows, err := p.Status(t.Context(), e.ID)
	if err != nil || len(rows) != 1 || rows[0].State != "queued" {
		t.Fatalf("rows %+v: %v", rows, err)
	}
}

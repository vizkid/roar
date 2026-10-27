package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/ping/internal/envelope"
	"github.com/vizkid/ping/internal/rex"
	"github.com/vizkid/ping/internal/rextest"
	"github.com/vizkid/ping/internal/store"
)

// These tests send through the cross-host path to the local Rex server: the
// mechanism (detached block, streamed stdin, exit code) is the same.

type fixture struct {
	host Host
	st   *store.Store
	to   envelope.Address
}

func setup(t *testing.T) *fixture {
	c := rextest.Require(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "ping")
	if b, err := exec.Command("go", "build", "-o", bin, "../../cmd/ping").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	home := filepath.Join(dir, "home")
	st, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	label := rextest.Label("dest")
	session, block := rextest.Session(t, c, label, "sleep", "600")
	w, _ := c.Whoami(t.Context())
	h := Host{Rex: c, Bin: bin, Env: []string{"PING_HOME=" + home}, Session: label + "-ping"}
	t.Cleanup(func() { killLabel(c, h.Session) })
	return &fixture{host: h, st: st, to: envelope.Address{HostID: w.HostID, Session: session, Block: block, Label: label}}
}

func killLabel(c rex.Client, label string) {
	ss, _ := c.Sessions(context.Background())
	for _, s := range ss {
		if s.Label == label {
			c.Run(context.Background(), "kill", s.ID)
		}
	}
}

func (f *fixture) env(text string) envelope.Envelope {
	return envelope.Envelope{
		ID: envelope.NewID(time.Now()), Kind: envelope.Inform, Text: text, CreatedAt: time.Now(),
		From: envelope.Address{HostID: "other-host", Session: "session:s", Block: "block:sender", Label: "sender"},
		To:   f.to,
	}
}

func TestEnqueueStoresAndDedupes(t *testing.T) {
	f := setup(t)
	e := f.env("across hosts")
	res, err := f.host.Enqueue(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	// The pane runs sleep, not an agent, so the delivery pass holds it.
	if res.State != "queued" || res.Reason != "wrong_program" {
		t.Fatalf("result %+v", res)
	}
	m, err := f.st.Get(e.ID)
	if err != nil || m.Envelope.Text != "across hosts" || !m.Envelope.Forwarded {
		t.Fatalf("stored %+v %v", m.Envelope, err)
	}
	if res, err := f.host.Enqueue(t.Context(), e); err != nil || res.State != "duplicate" {
		t.Fatalf("retry: %+v %v", res, err)
	}
}

func TestEnqueueStreamsLargeMessages(t *testing.T) {
	f := setup(t)
	text := strings.Repeat("0123456789abcdef\n", (envelope.MaxText-1024)/17)
	e := f.env(text)
	if _, err := f.host.Enqueue(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.st.Get(e.ID); m.Envelope.Text != text {
		t.Fatalf("stored %d bytes, sent %d", len(m.Envelope.Text), len(text))
	}
}

func TestEnqueueRefusesAnotherHostsMessage(t *testing.T) {
	f := setup(t)
	e := f.env("misrouted")
	e.To.HostID = "some-other-host"
	_, err := f.host.Enqueue(t.Context(), e)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "wrong_host") {
		t.Fatalf("err %v", err)
	}
}

func TestMissingPingOnHost(t *testing.T) {
	f := setup(t)
	f.host.Bin = "/nonexistent/ping"
	if _, err := f.host.Enqueue(t.Context(), f.env("x")); !errors.Is(err, ErrNoPing) {
		t.Fatalf("err %v", err)
	}
}

func TestRunOpensNoWindow(t *testing.T) {
	f := setup(t)
	f.host.Enqueue(t.Context(), f.env("warm up")) // creates the ping session
	windows := func() int {
		ss, _ := f.host.Rex.Sessions(t.Context())
		for _, s := range ss {
			if s.Label == f.host.Session {
				var out struct{ Windows []any }
				f.host.Rex.JSON(t.Context(), &out, "window", "ls", "-s", s.ID, "--json")
				return len(out.Windows)
			}
		}
		return -1
	}
	before := windows()
	f.host.Enqueue(t.Context(), f.env("again"))
	if after := windows(); after != before || before < 0 {
		t.Fatalf("windows %d -> %d", before, after)
	}
}

func TestStatusAcrossHosts(t *testing.T) {
	f := setup(t)
	e := f.env("x")
	f.host.Enqueue(t.Context(), e)
	rows, err := f.host.Status(t.Context(), e.ID)
	if err != nil || len(rows) != 1 || rows[0].State != "queued" {
		t.Fatalf("rows %+v err %v", rows, err)
	}
}

func TestEnqueueCarriesAttachments(t *testing.T) {
	f := setup(t)
	e := f.env("with a file")
	data := []byte(strings.Repeat("attachment ", 20000))
	e.Files = []envelope.File{envelope.FileFor("blob.txt", data)}
	if _, err := f.host.EnqueueWithFiles(t.Context(), e, map[string][]byte{"blob.txt": data}); err != nil {
		t.Fatal(err)
	}
	m, err := f.st.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(m.Dir, "files", "blob.txt"))
	if string(got) != string(data) {
		t.Fatalf("stored %d bytes, sent %d", len(got), len(data))
	}
}

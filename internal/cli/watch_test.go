package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("after %s: %s", within, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// daemon runs roard on the harness's store, as the service manager would,
// with its own client label so it never meets a real roard.
func (h *harness) daemon() {
	h.t.Helper()
	vars := map[string]string{"ROAR_HOME": h.home, "ROAR_ROARD_LABEL": rextest.Label("roard"), "HOME": h.t.TempDir()}
	var log bytes.Buffer
	env := Env{Stdout: &log, Stderr: &log, Getenv: func(k string) string { return vars[k] }, Now: time.Now, Rex: h.rex}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cmdDaemon(ctx, env, nil) }()
	h.t.Cleanup(func() { cancel(); <-done })
	st, err := store.Open(h.home)
	if err != nil {
		h.t.Fatal(err)
	}
	eventually(h.t, 10*time.Second, "roard did not start", st.DaemonLive)
}

// The incident: an inform to a busy agent without a native adapter. The
// send holds it; roard delivers it when the agent comes to rest, with no
// further command and no Rex block.
func TestRoardDeliversHeldSend(t *testing.T) {
	h := newHarness(t)
	h.daemon()
	sender := h.pane("sender")
	a, _ := h.agent("claude")
	a.Control("working")
	code, out, errs := h.run(sender, "", "send", "--json", a.Label, "while", "you", "work")
	if code != 0 || !strings.Contains(out, `"hold_reason":"busy"`) {
		t.Fatalf("held send: %d %s %s", code, out, errs)
	}
	if strings.Contains(errs, "roard_not_running") {
		t.Fatalf("warned with roard running: %s", errs)
	}
	var res struct{ ID string }
	json.Unmarshal([]byte(out), &res)
	st, err := store.Open(h.home)
	if err != nil {
		t.Fatal(err)
	}
	a.Control("done")
	eventually(t, 20*time.Second, "roard did not deliver", func() bool {
		m, _ := st.Get(res.ID)
		return m.Record.State == store.Submitted
	})
	if got := a.Submitted(); len(got) != 1 || !strings.HasSuffix(got[0], "\nwhile you work") {
		t.Fatalf("agent received %q", got)
	}
}

// 52: with no roard, a held message says it waits for one.
func TestHeldSendWarnsWithoutRoard(t *testing.T) {
	h := newHarness(t)
	sender := h.pane("sender")
	a, _ := h.agent("claude")
	a.Control("working")
	code, _, errs := h.run(sender, "", "send", a.Label, "later")
	if code != 0 || !strings.Contains(errs, "roard_not_running") {
		t.Fatalf("held send without roard: %d %s", code, errs)
	}
}

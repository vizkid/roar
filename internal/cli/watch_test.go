package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

// selfBuild builds roar for commands that start a watcher, and gives them a
// relay session of their own so tests never touch roar-relay.
func (h *harness) selfBuild() {
	h.t.Helper()
	h.self = filepath.Join(h.t.TempDir(), "roar")
	if b, err := exec.Command("go", "build", "-o", h.self, "../../cmd/roar").CombinedOutput(); err != nil {
		h.t.Fatalf("build: %v %s", err, b)
	}
	h.relay = rextest.Label("relay")
	h.t.Cleanup(func() { exec.Command("rex", "kill", h.relay).Run() })
}

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

// The incident: an inform to a busy agent without a native adapter. The
// send holds it and starts a watcher; the watcher delivers it when the
// agent comes to rest, with no further command, and then exits.
func TestSendStartsWatcherThatDeliversAndExits(t *testing.T) {
	h := newHarness(t)
	h.selfBuild()
	sender := h.pane("sender")
	a, _ := h.agent("claude")
	a.Control("working")
	code, out, errs := h.run(sender, "", "send", "--json", a.Label, "while", "you", "work")
	if code != 0 || !strings.Contains(out, `"hold_reason":"busy"`) {
		t.Fatalf("held send: %d %s %s", code, out, errs)
	}
	var res struct{ ID string }
	json.Unmarshal([]byte(out), &res)
	st, err := store.Open(h.home)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "no watcher took watch.lock", func() bool { return !st.WatcherFree() })

	a.Control("done")
	eventually(t, 20*time.Second, "the watcher did not deliver", func() bool {
		m, _ := st.Get(res.ID)
		return m.Record.State == store.Submitted
	})
	if got := a.Submitted(); len(got) != 1 || !strings.HasSuffix(got[0], "\nwhile you work") {
		t.Fatalf("agent received %q", got)
	}
	eventually(t, 45*time.Second, "the watcher did not exit", st.WatcherFree)
}

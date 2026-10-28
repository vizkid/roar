package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

// runAs runs one roar command with exactly these variables, as Codex's
// daemon would: REX_* may name a pane the command does not run in.
func (h *harness) runAs(vars map[string]string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	vars["ROAR_HOME"] = h.home
	code := Main(args, Env{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return vars[k] },
		Now:    time.Now, Rex: h.rex,
	})
	return code, out.String(), errb.String()
}

// codexPane is a pane whose foreground Rex reports as Codex.
func (h *harness) codexPane(name string) pane {
	fake := filepath.Join(h.t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/usr/bin/python3\nimport time\ntime.sleep(600)\n"), 0o755); err != nil {
		h.t.Fatal(err)
	}
	label := rextest.Label(name)
	s, b := rextest.Session(h.t, h.rex, label, fake)
	time.Sleep(500 * time.Millisecond)
	return pane{label, s, b}
}

func TestCodexThreadIdentity(t *testing.T) {
	h := newHarness(t)
	cx, other := h.codexPane("cx"), h.pane("other")
	st, _ := store.Open(h.home)
	proc, err := h.rex.Process(t.Context(), cx.session, cx.block)
	if err != nil || proc.Foreground == nil {
		t.Fatal(err)
	}
	// The daemon's environment names a dead pane.
	daemon := func(thread string) map[string]string {
		return map[string]string{"REX_SESSION": "session:dead", "REX_BLOCK": "block:dead",
			"CODEX_THREAD_ID": thread, "CODEX_HOME": "/nonexistent"}
	}
	sender := func(id string) string {
		m, err := st.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return m.Envelope.From.Block
	}

	// 1. A live binding for the thread names the pane.
	st.BindCodex(cx.block, store.CodexBinding{Thread: "T1", Session: cx.session, PID: proc.Foreground.PID})
	code, out, errs := h.runAs(daemon("T1"), "send", other.label, "hi")
	if code != 0 || sender(strings.Fields(out)[0]) != cx.block {
		t.Fatalf("bound thread: %d %q %q", code, out, errs)
	}

	// 2. No binding and no proof: refused, never sent as the dead pane.
	code, _, errs = h.runAs(daemon("T9"), "send", other.label, "hi")
	if code != 2 || !strings.Contains(errs, "unbound_codex_thread") {
		t.Fatalf("unbound thread: %d %q", code, errs)
	}

	// 3. The thread shows the header of the last message delivered to cx:
	// that proves the thread is in cx, and Roar records the binding.
	old := h.send(other, "send", cx.label, "older")
	last := h.send(other, "send", cx.label, "newest")
	for _, id := range []string{old, last} {
		c, _ := st.Claim(id)
		c.Move(store.Written, time.Now(), func(r *store.Record) { r.WrittenAt = time.Now() })
		c.Release()
		time.Sleep(10 * time.Millisecond)
	}
	defer func(f func(context.Context, string, string) ([]string, error)) { deliveredIDs = f }(deliveredIDs)
	deliveredIDs = func(_ context.Context, home, thread string) ([]string, error) {
		return map[string][]string{"T5": {last}, "T6": {old}}[thread], nil
	}
	code, out, errs = h.runAs(daemon("T5"), "send", other.label, "hi")
	if code != 0 || sender(strings.Fields(out)[0]) != cx.block {
		t.Fatalf("proved thread: %d %q %q", code, out, errs)
	}
	if b, _ := st.CodexBinding(cx.block); b.Thread != "T5" || b.PID != proc.Foreground.PID {
		t.Fatalf("binding after proof %+v", b)
	}
	// An older message proves nothing: the pane may have moved on with /new.
	if code, _, errs = h.runAs(daemon("T6"), "send", other.label, "hi"); code != 2 {
		t.Fatalf("stale proof accepted: %d %q", code, errs)
	}
}

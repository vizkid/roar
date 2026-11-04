package cli

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// codexPID is the foreground PID of a test Codex pane.
func (h *harness) codexPID(p pane) int {
	h.t.Helper()
	proc, err := h.rex.Process(h.t.Context(), p.session, p.block)
	if err != nil || proc.Foreground == nil {
		h.t.Fatalf("no foreground: %v", err)
	}
	return proc.Foreground.PID
}

// Burns's launch watcher sees the thread a new Codex TUI starts on the
// daemon and binds it, with the pane's own REX_BLOCK and the Codex PID. It
// is not a child of Codex, so the binding rests on the PID, not ancestry.
func TestBindCodexAtLaunch(t *testing.T) {
	h := newHarness(t)
	_, p := h.agent("codex")
	pid := h.codexPID(p)
	code, out, errs := h.run(p, "", "bind", "codex", "--thread", "019a-thread-1", "--pid", strconv.Itoa(pid), "--codex-home", "/h/.codex-x")
	if code != 0 || strings.TrimSpace(out) != "bound "+p.block {
		t.Fatalf("bind: %d %q %q", code, out, errs)
	}
	st, _ := store.Open(h.home)
	b, ok := st.CodexBinding(p.block)
	want := store.CodexBinding{Thread: "019a-thread-1", Session: p.session, CodexHome: "/h/.codex-x", PID: pid, At: b.At}
	if !ok || b != want || b.At.IsZero() {
		t.Fatalf("binding %+v, want %+v", b, want)
	}
	if block, _, ok := st.CodexThread("019a-thread-1"); !ok || block != p.block {
		t.Fatalf("thread lookup %q %v", block, ok)
	}
}

// A wrong PID, a pane that does not run Codex, a missing pane or a bad
// argument binds nothing: a wrong binding would deliver this pane's
// messages into another thread.
func TestBindCodexRefuses(t *testing.T) {
	h := newHarness(t)
	_, p := h.agent("codex")
	pid := strconv.Itoa(h.codexPID(p))
	shell := h.pane("shell")
	shellPID := strconv.Itoa(h.codexPID(shell))
	cases := []struct {
		name string
		p    pane
		args []string
		want string
	}{
		{"wrong pid", p, []string{"--thread", "t1", "--pid", "1"}, "pid_not_foreground"},
		{"not codex", shell, []string{"--thread", "t1", "--pid", shellPID}, "not_codex_pane"},
		{"outside rex", pane{}, []string{"--thread", "t1", "--pid", pid}, "not_in_rex_pane"},
		{"no thread", p, []string{"--pid", pid}, "usage"},
		{"no pid", p, []string{"--thread", "t1"}, "usage"},
		{"bad pid", p, []string{"--thread", "t1", "--pid", "x"}, "usage"},
		{"bad thread", p, []string{"--thread", "../t1", "--pid", pid}, "invalid_thread"},
	}
	for _, c := range cases {
		code, out, errs := h.run(c.p, "", append([]string{"bind", "codex"}, c.args...)...)
		if code == 0 || !strings.Contains(errs, c.want) || out != "" {
			t.Errorf("%s: %d %q %q", c.name, code, out, errs)
		}
	}
	st, _ := store.Open(h.home)
	if b, ok := st.CodexBinding(p.block); ok {
		t.Fatalf("bound after refusals: %+v", b)
	}
}

// A thread lives in one pane. A binding for it in a pane that still runs
// that Codex wins; one whose Codex is gone is stale and is removed, so the
// thread lookup cannot find it first.
func TestBindCodexThreadInOnePane(t *testing.T) {
	h := newHarness(t)
	_, a := h.agent("codex")
	_, b := h.agent("codex")
	st, _ := store.Open(h.home)
	st.BindCodex(a.block, store.CodexBinding{Thread: "t1", Session: a.session, PID: h.codexPID(a), At: time.Now()})
	code, _, errs := h.run(b, "", "bind", "codex", "--thread", "t1", "--pid", strconv.Itoa(h.codexPID(b)))
	if code == 0 || !strings.Contains(errs, "thread_bound_elsewhere") {
		t.Fatalf("live elsewhere: %d %q", code, errs)
	}
	st.BindCodex(a.block, store.CodexBinding{Thread: "t1", Session: a.session, PID: 1, At: time.Now()})
	if code, _, errs := h.run(b, "", "bind", "codex", "--thread", "t1", "--pid", strconv.Itoa(h.codexPID(b))); code != 0 {
		t.Fatalf("stale elsewhere: %d %q", code, errs)
	}
	if _, ok := st.CodexBinding(a.block); ok {
		t.Fatal("stale binding kept")
	}
	if block, _, _ := st.CodexThread("t1"); block != b.block {
		t.Fatalf("thread in %q, want %q", block, b.block)
	}
}

package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

// TestLiveCodexChannel drives a plain Codex in a Rex pane. It needs
// ROAR_LIVE=1, ROAR_LIVE_DIR, and a Codex home where `roar setup` ran and
// the operator trusted the Roar hook with /hooks. It spends a few short
// model turns.
func TestLiveCodexChannel(t *testing.T) {
	if os.Getenv("ROAR_LIVE") != "1" || os.Getenv("ROAR_LIVE_DIR") == "" {
		t.Skip("set ROAR_LIVE=1 and ROAR_LIVE_DIR to run live agent tests")
	}
	h := newHarness(t)
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not on PATH")
	}
	cmd := []string{"env", "-C", os.Getenv("ROAR_LIVE_DIR"), "ROAR_HOME=" + h.home}
	if v := os.Getenv("CODEX_HOME"); v != "" {
		cmd = append(cmd, "CODEX_HOME="+v)
	}
	label := rextest.Label("codex")
	session, block := rextest.Session(t, h.rex, label, append(cmd, codexBin)...)
	sender := h.pane("sender")
	waitAgentRest(t, h, session, block)
	st, _ := store.Open(h.home)
	var bound store.CodexBinding
	if !waitFor(10*time.Second, func() bool { b, ok := st.CodexBinding(block); bound = b; return ok }) {
		t.Fatal("no binding: run roar setup and trust the Roar hook with /hooks in this Codex home")
	}
	screen := func() string {
		b, _ := h.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
		return string(b)
	}
	settle := func(id string) store.Record {
		t.Helper()
		var rec store.Record
		if !waitFor(60*time.Second, func() bool {
			m, _ := st.Get(id)
			rec = m.Record
			return rec.State.Final() || rec.State == store.Unknown
		}) {
			t.Fatalf("%s stayed %+v\n%s", id, rec, screen())
		}
		if rec.State != store.Read {
			t.Fatalf("%s ended %+v\n%s", id, rec, screen())
		}
		return rec
	}

	// 1. Idle: a new turn.
	id := h.send(sender, "send", label, "Roar live test one: reply with the word OK only.")
	if rec := settle(id); rec.Proof != "codex_turn_start" {
		t.Fatalf("idle receipt %+v", rec)
	}
	waitAgentRest(t, h, session, block)

	// 2. Busy: the second message steers the running turn.
	long := h.send(sender, "send", label, "Write the numbers from 1 to 150, one per line, then the word END.")
	settle(long)
	waitFor(20*time.Second, func() bool {
		s, _ := h.rex.ProgramStatus(t.Context(), session, block)
		return s != nil && s.State == "working"
	})
	steer := h.send(sender, "send", label, "Roar live test steer: after END, also write STEERED.")
	if rec := settle(steer); rec.Proof != "codex_turn_steer" || rec.Boundary != "mid_turn" {
		t.Logf("second message receipt %+v (a turn that ended first is a turn/start)", rec)
	}
	waitAgentRest(t, h, session, block)

	// 3. /new: the message goes to the thread the TUI now shows.
	h.rex.Run(t.Context(), "send", "-s", session, "-b", block, "/new")
	time.Sleep(500 * time.Millisecond)
	h.rex.Run(t.Context(), "send-key", "-s", session, "-b", block, "enter")
	time.Sleep(3 * time.Second)
	waitAgentRest(t, h, session, block)
	if !waitFor(10*time.Second, func() bool { b, _ := st.CodexBinding(block); return b.Thread != bound.Thread }) {
		t.Fatal("/new did not rebind the pane")
	}
	id = h.send(sender, "send", label, "Roar live test after new: reply with the word FRESH only.")
	settle(id)
	if !waitFor(30*time.Second, func() bool { return strings.Contains(screen(), "Roar live test after new") }) {
		t.Fatalf("message after /new is not in the visible thread:\n%s", screen())
	}
	t.Logf("settled pane:\n%s", screen())
}

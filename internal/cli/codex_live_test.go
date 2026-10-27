package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/ping/internal/rextest"
	"github.com/vizkid/ping/internal/store"
)

// TestLiveCodexChannel drives real Codex through `ping codex`. It needs
// PING_LIVE=1 and PING_LIVE_DIR, and spends a few short model turns.
func TestLiveCodexChannel(t *testing.T) {
	if os.Getenv("PING_LIVE") != "1" || os.Getenv("PING_LIVE_DIR") == "" {
		t.Skip("set PING_LIVE=1 and PING_LIVE_DIR to run live agent tests")
	}
	h := newHarness(t)
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not on PATH")
	}
	pingBin := filepath.Join(t.TempDir(), "ping")
	if b, err := exec.Command("go", "build", "-o", pingBin, "../../cmd/ping").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	cmd := []string{"env", "-C", os.Getenv("PING_LIVE_DIR"), "PING_HOME=" + h.home, "PING_CODEX_BIN=" + codexBin}
	if v := os.Getenv("CODEX_HOME"); v != "" {
		cmd = append(cmd, "CODEX_HOME="+v)
	}
	label := rextest.Label("codex")
	session, block := rextest.Session(t, h.rex, label, append(cmd, pingBin, "codex")...)
	sender := h.pane("sender")
	waitAgentRest(t, h, session, block)
	st, _ := store.Open(h.home)
	if !st.AdapterLive(block) {
		t.Fatal("ping codex holds no adapter lock")
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
	id := h.must(sender, "send", label, "Ping live test one: reply with the word OK only.")
	if rec := settle(id); rec.Proof != "codex_turn_start" {
		t.Fatalf("idle receipt %+v", rec)
	}
	waitAgentRest(t, h, session, block)

	// 2. Busy: the second message steers the running turn.
	long := h.must(sender, "send", label, "Write the numbers from 1 to 150, one per line, then the word END.")
	settle(long)
	waitFor(20*time.Second, func() bool {
		s, _ := h.rex.ProgramStatus(t.Context(), session, block)
		return s != nil && s.State == "working"
	})
	steer := h.must(sender, "send", label, "Ping live test steer: after END, also write STEERED.")
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
	id = h.must(sender, "send", label, "Ping live test after new: reply with the word FRESH only.")
	settle(id)
	if !waitFor(30*time.Second, func() bool { return strings.Contains(screen(), "Ping live test after new") }) {
		t.Fatalf("message after /new is not in the visible thread:\n%s", screen())
	}
	t.Logf("settled pane:\n%s", screen())
}

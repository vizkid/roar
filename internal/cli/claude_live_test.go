package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

// TestLiveClaudeChannel drives real Claude Code through `roar claude`. It
// needs ROAR_LIVE=1 and ROAR_LIVE_DIR (a directory Claude already trusts),
// and spends a few short model turns.
func TestLiveClaudeChannel(t *testing.T) {
	if os.Getenv("ROAR_LIVE") != "1" || os.Getenv("ROAR_LIVE_DIR") == "" {
		t.Skip("set ROAR_LIVE=1 and ROAR_LIVE_DIR to run live agent tests")
	}
	h := newHarness(t)
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	roarBin := filepath.Join(t.TempDir(), "roar")
	if b, err := exec.Command("go", "build", "-o", roarBin, "../../cmd/roar").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	cmd := []string{"env", "-C", os.Getenv("ROAR_LIVE_DIR"), "ROAR_HOME=" + h.home, "ROAR_CLAUDE_BIN=" + claudeBin}
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		cmd = append(cmd, "CLAUDE_CONFIG_DIR="+v)
	}
	cmd = append(cmd, roarBin, "claude", "--model", "haiku", "--strict-mcp-config",
		"--settings", `{"disableAllHooks":true}`, "--allowedTools", "Bash(sleep:*)")
	label := rextest.Label("claude")
	session, block := rextest.Session(t, h.rex, label, cmd...)
	agent := pane{label, session, block}
	sender := h.pane("sender")

	acceptConsent(t, h, session, block)
	waitAgentRest(t, h, session, block)
	if !waitFor(10*time.Second, func() bool { st, _ := store.Open(h.home); return st.AdapterLive(block) }) {
		t.Fatal("roar mcp never took the adapter lock")
	}
	st, _ := store.Open(h.home)
	settle := func(id string, want store.State) store.Record {
		t.Helper()
		var rec store.Record
		if !waitFor(90*time.Second, func() bool { m, _ := st.Get(id); rec = m.Record; return rec.State == want }) {
			screen, _ := h.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
			t.Fatalf("%s stayed %+v, want %s\n%s", id, rec, want, screen)
		}
		return rec
	}

	// 1. Idle: the message starts a turn.
	id := h.must(sender, "send", label, "Roar live test: reply with the word OK only.")
	if rec := settle(id, store.Read); rec.Boundary != "turn" || rec.Proof != "claude_transcript" {
		t.Fatalf("idle receipt %+v", rec)
	}
	waitAgentRest(t, h, session, block)

	// 2. Mid-turn: a note sent while Claude runs a tool is absorbed in the turn,
	// and a request waits until Claude is at rest.
	busy := h.must(sender, "send", label, "Run the shell command `sleep 12`, then reply DONE.")
	settle(busy, store.Read)
	waitFor(20*time.Second, func() bool {
		s, _ := h.rex.ProgramStatus(t.Context(), session, block)
		return s != nil && s.State == "working"
	})
	note := h.must(sender, "send", label, "Mid-turn note for the live test; no reply needed.")
	req := h.must(sender, "send", "--request", label, "Request for the live test: reply with the word YES only.")
	time.Sleep(2 * time.Second)
	if m, _ := st.Get(req); m.Record.State != store.Queued || m.Record.HoldReason != "busy" {
		t.Fatalf("request while working: %+v", m.Record)
	}
	if rec := settle(note, store.Read); rec.Boundary != "mid_turn" {
		t.Fatalf("mid-turn receipt %+v", rec)
	}
	if rec := settle(req, store.Read); rec.Boundary != "turn" {
		t.Fatalf("request receipt %+v", rec)
	}
	waitAgentRest(t, h, session, block)

	// 3. After /clear, Claude writes a new transcript; receipt follows it.
	h.rex.Run(t.Context(), "send", "-s", session, "-b", block, "/clear")
	h.rex.Run(t.Context(), "send-key", "-s", session, "-b", block, "enter")
	time.Sleep(3 * time.Second)
	waitAgentRest(t, h, session, block)
	id = h.must(sender, "send", label, "After clear: reply with the word OK only.")
	settle(id, store.Read)

	screen, _ := h.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
	t.Logf("settled pane:\n%s", screen)
	_ = agent
}

// acceptConsent answers Claude's development-channel dialog, only when it
// names exactly the Roar channel. Tests act as the operator here.
func acceptConsent(t *testing.T, h *harness, session, block string) {
	t.Helper()
	var screen string
	ok := waitFor(60*time.Second, func() bool {
		b, _ := h.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
		screen = string(b)
		return strings.Contains(screen, "Loading development channels") && strings.Contains(screen, "I am using this for local development")
	})
	if !ok {
		t.Fatalf("no consent dialog:\n%s", screen)
	}
	if !strings.Contains(screen, "Channels: server:roar") || strings.Count(screen, "server:") != 1 {
		t.Fatalf("unexpected channel list:\n%s", screen)
	}
	h.rex.Run(t.Context(), "send-key", "-s", session, "-b", block, "enter")
}

func waitAgentRest(t *testing.T, h *harness, session, block string) {
	t.Helper()
	since := time.Time{}
	ok := waitFor(90*time.Second, func() bool {
		s, err := h.rex.ProgramStatus(t.Context(), session, block)
		if err != nil || s == nil || (s.State != "idle" && s.State != "done") {
			since = time.Time{}
			return false
		}
		if since.IsZero() {
			since = time.Now()
		}
		return time.Since(since) > 2*time.Second
	})
	if !ok {
		screen, _ := h.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
		t.Fatalf("agent never came to rest:\n%s", screen)
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

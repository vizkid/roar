package fallback

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

// Live tests drive real Claude Code and Codex. They need ROAR_LIVE=1 and
// ROAR_LIVE_DIR, a directory both agents already trust (Claude shows a
// folder-trust dialog in a new directory, and the proof rightly waits on it).
// Each test spends one trivial model turn.

func liveDir(t *testing.T) string {
	if os.Getenv("ROAR_LIVE") != "1" {
		t.Skip("set ROAR_LIVE=1 to run live agent tests")
	}
	dir := os.Getenv("ROAR_LIVE_DIR")
	if dir == "" {
		t.Skip("set ROAR_LIVE_DIR to a directory the agents trust")
	}
	return dir
}

func liveAgent(t *testing.T, f *fixture, dir string, cmd ...string) (session, block string) {
	t.Helper()
	// Rex runs pane commands in its server's login environment, not ours,
	// so carry the agent homes across explicitly.
	full := []string{"env", "-C", dir}
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		if v := os.Getenv(k); v != "" {
			full = append(full, k+"="+v)
		}
	}
	full = append(full, cmd...)
	session, block = rextest.Session(t, f.rex, rextest.Label("live"), full...)
	skipStartupDialogs(t, f, session, block)
	// Codex reports idle, then works briefly while it starts up. Wait until
	// the agent has stayed at rest for two seconds.
	deadline := time.Now().Add(60 * time.Second)
	restSince := time.Time{}
	for time.Now().Before(deadline) {
		st, err := f.rex.ProgramStatus(t.Context(), session, block)
		atRest := err == nil && st != nil && (st.State == "idle" || st.State == "done")
		switch {
		case !atRest:
			restSince = time.Time{}
		case restSince.IsZero():
			restSince = time.Now()
		case time.Since(restSince) > 2*time.Second:
			return session, block
		}
		time.Sleep(250 * time.Millisecond)
	}
	screen, _ := f.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
	t.Fatalf("agent never came to rest:\n%s", screen)
	return
}

func liveDeliver(t *testing.T, f *fixture, session, block string) {
	t.Helper()
	e := f.queue(session, block, "Roar live test: reply with the word OK only.")
	want(t, f.attempt(e.ID), store.Submitted, "")
	time.Sleep(3 * time.Second)
	screen, _ := f.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
	if !strings.Contains(string(screen), "Roar live test") {
		t.Fatalf("message not on screen:\n%s", screen)
	}
	t.Logf("settled pane:\n%s", screen)

	// A draft must hold the next message.
	if _, err := f.rex.Run(t.Context(), "send", "-s", session, "-b", block, "operator draft"); err != nil {
		t.Fatal(err)
	}
	waitRest(t, f, session, block)
	time.Sleep(500 * time.Millisecond)
	e = f.queue(session, block, "must not be typed")
	want(t, f.attempt(e.ID), store.Queued, "draft")
}

func waitRest(t *testing.T, f *fixture, session, block string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := f.rex.ProgramStatus(t.Context(), session, block)
		if st != nil && (st.State == "idle" || st.State == "done") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("agent stayed busy")
}

func TestLiveClaudeFallback(t *testing.T) {
	dir := liveDir(t)
	f := setup(t)
	session, block := liveAgent(t, f, dir, "claude", "--model", "haiku", "--strict-mcp-config",
		"--settings", `{"disableAllHooks":true}`)
	liveDeliver(t, f, session, block)
}

func TestLiveCodexFallback(t *testing.T) {
	dir := liveDir(t)
	f := setup(t)
	session, block := liveAgent(t, f, dir, "codex")
	liveDeliver(t, f, session, block)
}

// A real Codex mid-turn: the composer stays live, Enter adds the message to
// the running turn, and the model acts on it before the turn ends.
func TestLiveCodexMidTurn(t *testing.T) {
	dir := liveDir(t)
	f := setup(t)
	session, block := liveAgent(t, f, dir, "codex", "-a", "never", "-s", "read-only")
	task := "Run the shell command `sleep 25`, then reply with the word FIRST."
	if _, err := f.rex.Run(t.Context(), "send", "-s", session, "-b", block, task); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if _, err := f.rex.Run(t.Context(), "send-key", "-s", session, "-b", block, "enter"); err != nil {
		t.Fatal(err)
	}
	waitState(t, f, session, block, "working")
	time.Sleep(5 * time.Second) // into the sleep, well before the turn ends
	e := f.queue(session, block, "Roar live steer: when you reply, add the word STEERED.")
	want(t, f.attempt(e.ID), store.Submitted, "")
	if m, _ := f.st.Get(e.ID); m.Record.Proof != "rex_steer" {
		t.Fatalf("proof %q, want rex_steer", m.Record.Proof)
	}
	waitRest(t, f, session, block)
	screen, _ := f.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
	if !strings.Contains(string(screen), "STEERED") {
		t.Fatalf("the model did not act on the steer:\n%s", screen)
	}
	t.Logf("settled pane:\n%s", screen)
}

func waitState(t *testing.T, f *fixture, session, block, state string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := f.rex.ProgramStatus(t.Context(), session, block); st != nil && st.State == state {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("agent never reported %s", state)
}

// skipStartupDialogs presses Esc on Codex's update and hook-review dialogs.
// Esc skips each once and saves no choice, so hook trust stays unchanged.
func skipStartupDialogs(t *testing.T, f *fixture, session, block string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		screen, _ := f.rex.Run(t.Context(), "capture", "-s", session, "-b", block, "--trim")
		if strings.Contains(string(screen), "esc skip") {
			f.rex.Run(t.Context(), "send-key", "-s", session, "-b", block, "escape")
			time.Sleep(time.Second)
			continue
		}
		if st, _ := f.rex.ProgramStatus(t.Context(), session, block); st != nil && st.State == "idle" {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

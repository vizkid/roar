// Package agenttest runs the test agent (internal/testagent) in real Rex
// panes and reads back what it received.
package agenttest

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/rextest"
)

// Agent is one test agent running in its own Rex session.
type Agent struct {
	t                     *testing.T
	Label, Session, Block string
	log, ctl              string
}

// Build compiles the test agent as dir/claude, dir/codex, dir/devin and dir/agy.
func Build(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Join(filepath.Dir(file), "..", "testagent")
	out := filepath.Join(dir, "claude")
	if b, err := exec.Command("go", "build", "-o", out, src).CombinedOutput(); err != nil {
		t.Fatalf("building test agent: %v\n%s", err, b)
	}
	// A copy, not a hard link: macOS may report either link's path for a
	// hard-linked binary, so the process name could flip between the two.
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "devin", "agy"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Start runs the agent named kind ("claude" or "codex") from dir in a new
// session. env adds TESTAGENT_* settings.
func Start(t *testing.T, c rex.Client, dir, kind string, env ...string) *Agent {
	t.Helper()
	work := t.TempDir()
	a := &Agent{t: t, Label: rextest.Label(kind), log: filepath.Join(work, "log"), ctl: filepath.Join(work, "ctl")}
	cmd := []string{"env", "TESTAGENT_LOG=" + a.log, "TESTAGENT_CTL=" + a.ctl}
	cmd = append(append(cmd, env...), filepath.Join(dir, kind))
	a.Session, a.Block = rextest.Session(t, c, a.Label, cmd...)
	a.WaitStatus(c, "idle")
	return a
}

// Control sends one control word (idle, working, done, blocked, clear, exit).
func (a *Agent) Control(word string) {
	a.t.Helper()
	if err := os.WriteFile(a.ctl, []byte(word), 0o600); err != nil {
		a.t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
}

// WaitStatus waits until Rex reports the given program status.
func (a *Agent) WaitStatus(c rex.Client, state string) {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := c.ProgramStatus(a.t.Context(), a.Session, a.Block)
		if err == nil && st != nil && st.State == state {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.t.Fatalf("agent never reported %s", state)
}

// Submitted is every prompt the agent received through Enter, in order.
func (a *Agent) Submitted() []string {
	f, err := os.Open(a.log)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct{ Text string }
		if json.Unmarshal(sc.Bytes(), &line) == nil {
			out = append(out, line.Text)
		}
	}
	return out
}

// Type puts a draft in the composer the way a person would.
func (a *Agent) Type(c rex.Client, text string) {
	a.t.Helper()
	if _, err := c.Run(a.t.Context(), "send", "-s", a.Session, "-b", a.Block, text); err != nil {
		a.t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
}

// Joined is a helper for error messages.
func Joined(s []string) string { return strings.Join(s, " | ") }

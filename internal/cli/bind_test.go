package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

var agyVars = map[string]string{
	"ANTIGRAVITY_CONVERSATION_ID": "C1",
	"ANTIGRAVITY_LS_ADDRESS":      "localhost:4242",
	"ANTIGRAVITY_CSRF_TOKEN":      "secret-token",
	"ANTIGRAVITY_AGENTAPI_EXE":    "/opt/agy",
}

// runAgy runs one roar command in pane p with agy's variables added.
func (h *harness) runAgy(p pane, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	vars := map[string]string{"ROAR_HOME": h.home, "REX_SESSION": p.session, "REX_BLOCK": p.block}
	for k, v := range agyVars {
		vars[k] = v
	}
	code = Main(args, Env{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return vars[k] },
		Now:    time.Now, Rex: h.rex, Self: h.self, Relay: h.relay,
	})
	return code, out.String(), errb.String()
}

// fakeAgy runs the shell command script from a program named agy in a new pane, with agy's
// variables set, and returns the pane and a file the script may write.
func (h *harness) fakeAgy(script string) (pane, string, string) {
	h.t.Helper()
	dir := h.t.TempDir()
	roar := filepath.Join(dir, "roar")
	if out, err := exec.Command("go", "build", "-o", roar, "../../cmd/roar").CombinedOutput(); err != nil {
		h.t.Fatalf("build: %v %s", err, out)
	}
	agy := filepath.Join(dir, "agy")
	// Python, not sh: Rex reports a Python script's own path, so its pane runs "agy".
	py := "#!/usr/bin/python3\nimport os, time\nos.system(" + strconv.Quote(script) + ")\ntime.sleep(600)\n"
	if err := os.WriteFile(agy, []byte(py), 0o755); err != nil {
		h.t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out")
	cmd := []string{"env", "ROAR_HOME=" + h.home, "ROAR=" + roar, "OUT=" + outFile}
	for k, v := range agyVars {
		cmd = append(cmd, k+"="+v)
	}
	label := rextest.Label("agy")
	session, block := rextest.Session(h.t, h.rex, label, append(cmd, agy)...)
	return pane{label, session, block}, outFile, agy
}

func waitAgyBinding(t *testing.T, home, block string) (store.AgyBinding, bool) {
	st, _ := store.Open(home)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if b, ok := st.AgyBinding(block); ok {
			return b, true
		}
	}
	return store.AgyBinding{}, false
}

// A command the agy model runs carries the session's credentials. `roar
// bind agy` records them for the pane whose foreground agy is its ancestor.
func TestBindAgyOwnPane(t *testing.T) {
	h := newHarness(t)
	p, out, _ := h.fakeAgy(`"$ROAR" bind agy >"$OUT" 2>&1`)
	b, ok := waitAgyBinding(t, h.home, p.block)
	proc, err := h.rex.Process(t.Context(), p.session, p.block)
	if !ok || err != nil || proc.Foreground == nil {
		t.Fatalf("no binding: %v %v", ok, err)
	}
	want := store.AgyBinding{Conversation: "C1", Address: "localhost:4242", Token: "secret-token", Exe: "/opt/agy",
		Session: p.session, PID: proc.Foreground.PID, At: b.At}
	if b != want || b.At.IsZero() {
		t.Fatalf("binding %+v, want %+v", b, want)
	}
	time.Sleep(200 * time.Millisecond)
	printed, _ := os.ReadFile(out)
	if strings.TrimSpace(string(printed)) != "bound "+p.block {
		t.Fatalf("printed %q", printed)
	}
}

// Any other roar command agy runs binds too, and prints nothing about it.
func TestAgyCommandBindsSilently(t *testing.T) {
	h := newHarness(t)
	p, out, _ := h.fakeAgy(`"$ROAR" inbox >"$OUT" 2>&1`)
	if _, ok := waitAgyBinding(t, h.home, p.block); !ok {
		t.Fatal("inbox did not bind")
	}
	time.Sleep(200 * time.Millisecond)
	if printed, _ := os.ReadFile(out); strings.Contains(string(printed), "bound") || strings.Contains(string(printed), "secret") {
		t.Fatalf("printed %q", printed)
	}
}

func TestBindAgyRefusesOutsideAgy(t *testing.T) {
	h := newHarness(t)
	shell := h.pane("shell")
	code, out, errs := h.runAgy(shell, "bind", "agy")
	if code != 2 || !strings.Contains(errs, "not_agy_pane") || out != "" {
		t.Fatalf("shell pane: %d %q %q", code, out, errs)
	}
	code, _, errs = h.run(shell, "", "bind", "agy")
	if code != 2 || !strings.Contains(errs, "not_in_agy") {
		t.Fatalf("no agy variables: %d %q", code, errs)
	}
	if code, _, _ := h.run(shell, "", "bind", "claude"); code != 2 {
		t.Fatalf("bind claude: %d", code)
	}
	if entries, _ := os.ReadDir(filepath.Join(h.home, "agy")); len(entries) != 0 {
		t.Fatalf("wrote %d bindings", len(entries))
	}
}

// REX_BLOCK can be inherited. A command that agy did not start must not
// bind agy's pane.
func TestBindAgyIgnoresInheritedPane(t *testing.T) {
	h := newHarness(t)
	p, _, _ := h.fakeAgy("")
	time.Sleep(500 * time.Millisecond)
	code, _, errs := h.runAgy(p, "bind", "agy")
	if code != 2 || !strings.Contains(errs, "inherited_pane") || strings.Contains(errs, "secret") {
		t.Fatalf("inherited pane: %d %q", code, errs)
	}
	st, _ := store.Open(h.home)
	if b, ok := st.AgyBinding(p.block); ok {
		t.Fatalf("bound a pane this process does not run in: %+v", b)
	}
}

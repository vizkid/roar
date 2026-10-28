package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

func (h *harness) hook(vars map[string]string, stdin string) (int, string) {
	var out bytes.Buffer
	vars["ROAR_HOME"] = h.home
	code := Main([]string{"hook", "codex"}, Env{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &out,
		Getenv: func(k string) string { return vars[k] },
		Now:    time.Now, Rex: h.rex,
	})
	return code, out.String()
}

const startInput = `{"session_id":"T1","source":"startup","cwd":"/w","hook_event_name":"SessionStart","model":"m","permission_mode":"default","transcript_path":null}`

// The hook binds only a pane whose foreground process is its ancestor: an
// in-process Codex. A daemon-backed Codex runs hooks in the daemon, whose
// REX_BLOCK names whatever pane started the daemon.
func TestHookCodexBindsOwnPane(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	roar := filepath.Join(dir, "roar")
	if out, err := exec.Command("go", "build", "-o", roar, "../../cmd/roar").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	codex := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nprintf '%s' \"$HOOK_IN\" | \"$ROAR\" hook codex\nsleep 600\n"
	if err := os.WriteFile(codex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	label := rextest.Label("hook")
	session, block := rextest.Session(t, h.rex, label, "env", "ROAR_HOME="+h.home, "ROAR="+roar,
		"CODEX_HOME=/x/.codex-p", "HOOK_IN="+startInput, codex)
	st, _ := store.Open(h.home)
	var b store.CodexBinding
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		var ok bool
		if b, ok = st.CodexBinding(block); ok {
			break
		}
	}
	proc, err := h.rex.Process(t.Context(), session, block)
	if err != nil || proc.Foreground == nil {
		t.Fatal(err)
	}
	if b.Thread != "T1" || b.Session != session || b.CodexHome != "/x/.codex-p" || b.PID != proc.Foreground.PID || b.At.IsZero() {
		t.Fatalf("binding %+v; foreground %d", b, proc.Foreground.PID)
	}
}

func TestHookCodexIgnoresInheritedPane(t *testing.T) {
	h := newHarness(t)
	p := h.pane("hook")
	code, out := h.hook(map[string]string{"REX_SESSION": p.session, "REX_BLOCK": p.block}, startInput)
	if code != 0 || out != "" {
		t.Fatalf("exit %d, output %q", code, out)
	}
	st, _ := store.Open(h.home)
	if b, ok := st.CodexBinding(p.block); ok {
		t.Fatalf("bound a pane this process does not run in: %+v", b)
	}
}

func TestHookCodexOutsidePane(t *testing.T) {
	h := newHarness(t)
	for _, in := range []string{startInput, "not json"} {
		if code, out := h.hook(map[string]string{}, in); code != 0 || out != "" {
			t.Fatalf("exit %d, output %q", code, out)
		}
	}
	if entries, _ := os.ReadDir(h.home + "/codex"); len(entries) != 0 {
		t.Fatalf("wrote %d bindings", len(entries))
	}
}

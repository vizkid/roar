package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

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

func TestHookCodexBindsPane(t *testing.T) {
	h := newHarness(t)
	p := h.pane("hook")
	code, out := h.hook(map[string]string{"REX_SESSION": p.session, "REX_BLOCK": p.block, "CODEX_HOME": "/x/.codex-p"}, startInput)
	// Codex adds SessionStart output to the model's context.
	if code != 0 || out != "" {
		t.Fatalf("exit %d, output %q", code, out)
	}
	st, _ := store.Open(h.home)
	b, ok := st.CodexBinding(p.block)
	proc, err := h.rex.Process(t.Context(), p.session, p.block)
	if err != nil || proc.Foreground == nil {
		t.Fatal(err)
	}
	if !ok || b.Thread != "T1" || b.CodexHome != "/x/.codex-p" || b.PID != proc.Foreground.PID || b.At.IsZero() {
		t.Fatalf("binding %+v, %v; foreground %d", b, ok, proc.Foreground.PID)
	}

	// Without CODEX_HOME, Codex uses ~/.codex.
	h.hook(map[string]string{"REX_SESSION": p.session, "REX_BLOCK": p.block, "HOME": "/u"}, strings.Replace(startInput, "T1", "T2", 1))
	if b, _ := st.CodexBinding(p.block); b.Thread != "T2" || b.CodexHome != "/u/.codex" {
		t.Fatalf("rebind %+v", b)
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

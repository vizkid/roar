package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vizkid/roar/internal/ready"
)

// countingRex is a rex that logs each call's first word, then runs the real one.
func countingRex(t *testing.T) (bin string, calls func() []string) {
	t.Helper()
	real, err := exec.LookPath("rex")
	if err != nil {
		t.Skip("no rex on PATH")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "rex")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, func() []string {
		b, _ := os.ReadFile(log)
		return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "_"))
	}
}

// Review, 2026-11-03: a probe read the cursor, the row count and the whole
// screen before it looked at the status, so every busy pane cost three Rex
// calls it could not use.
func TestBusyProbeReadsNoScreen(t *testing.T) {
	h := newHarness(t)
	a, p := h.agent("claude")
	a.Control("working")
	a.WaitStatus(h.rex, "working")
	bin, calls := countingRex(t)
	rc := h.rex
	rc.Bin = bin
	if res := ready.Probe(t.Context(), rc, p.session, p.block, 0); res.Reason != "busy" {
		t.Fatalf("probe %+v", res)
	}
	if got := calls(); len(got) != 2 {
		t.Fatalf("a busy pane cost %d Rex calls, want 2 (process, status): %q", len(got), got)
	}
}

package cli

import (
	"encoding/json"
	"testing"

	"github.com/vizkid/roar/internal/store"
)

// `roar panes` shows, for each pane, how Roar would reach it and what
// waits there, so a stuck pane or a missing channel shows at a glance.
func TestPanesShowsDeliveryPaths(t *testing.T) {
	h := newHarness(t)
	sender, shell := h.pane("sender"), h.pane("shell")
	a, codexPane := h.agent("codex")
	_, claudePane := h.agent("claude")
	h.send(sender, "send", shell.label, "hello")

	st, _ := store.Open(h.home)
	p, err := h.rex.Process(t.Context(), a.Session, a.Block)
	if err != nil || p.Foreground == nil {
		t.Fatal(err)
	}
	if err := st.BindCodex(codexPane.block, store.CodexBinding{Thread: "T1", PID: p.Foreground.PID}); err != nil {
		t.Fatal(err)
	}
	lock, err := st.HoldAdapter(claudePane.block)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	out := h.must(sender, "panes", "--json")
	var rows []paneRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	byBlock := map[string]paneRow{}
	for _, r := range rows {
		byBlock[r.Block] = r
	}
	check := func(p pane, agent, path string, queued int) {
		t.Helper()
		r, ok := byBlock[p.block]
		if !ok || r.Agent != agent || r.Path != path || r.Queued != queued {
			t.Fatalf("%s: %+v (found %v), want agent %q path %q queued %d", p.label, r, ok, agent, path, queued)
		}
	}
	check(shell, "", "none", 1)
	// A binding alone is not enough: the thread must be on a Codex daemon.
	check(codexPane, "codex", "typed", 0)
	if r := byBlock[codexPane.block]; r.Note != "no_codex_daemon" {
		t.Fatalf("codex note %q", r.Note)
	}
	check(claudePane, "claude", "channel", 0)
	if r := byBlock[shell.block]; r.Ready != "refuse:wrong_program" {
		t.Fatalf("shell ready %q", r.Ready)
	}
}

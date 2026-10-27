package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vizkid/ping/internal/agenttest"
)

func (h *harness) agent(kind string, env ...string) (*agenttest.Agent, pane) {
	a := agenttest.Start(h.t, h.rex, h.agentBin(), kind, env...)
	return a, pane{a.Label, a.Session, a.Block}
}

func (h *harness) agentBin() string {
	if h.bin == "" {
		h.bin = agenttest.Build(h.t)
	}
	return h.bin
}

func TestSendDeliversByFallback(t *testing.T) {
	h := newHarness(t)
	sender := h.pane("sender")
	a, _ := h.agent("claude")
	code, out, errs := h.run(sender, "", "send", "--json", a.Label, "please", "review")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	var res struct{ ID, State string }
	json.Unmarshal([]byte(out), &res)
	if res.State != "submitted" {
		t.Fatalf("send result %s", out)
	}
	got := a.Submitted()
	if len(got) != 1 || !strings.HasSuffix(got[0], "\nplease review") || !strings.Contains(got[0], sender.label) {
		t.Fatalf("agent received %q", agenttest.Joined(got))
	}
}

func TestSendHoldsThenDeliverDelivers(t *testing.T) {
	h := newHarness(t)
	sender := h.pane("sender")
	a, _ := h.agent("codex")
	a.Control("working")
	code, out, _ := h.run(sender, "", "send", "--json", a.Label, "later")
	if code != 0 || !strings.Contains(out, `"hold_reason":"busy"`) {
		t.Fatalf("held send: %d %s", code, out)
	}
	a.Control("done")
	if got := h.must(sender, "deliver", a.Label); !strings.Contains(got, "submitted") {
		t.Fatalf("deliver printed %q", got)
	}
	if len(a.Submitted()) != 1 {
		t.Fatal("not delivered")
	}
}

func TestReadyExitCodes(t *testing.T) {
	h := newHarness(t)
	me := h.pane("me")
	a, _ := h.agent("claude")
	code, out, _ := h.run(me, "", "ready", a.Label)
	if code != 0 || strings.TrimSpace(out) != "empty" {
		t.Fatalf("idle agent: %d %q", code, out)
	}
	a.Type(h.rex, "draft")
	code, out, _ = h.run(me, "", "ready", a.Label)
	if code != 1 || strings.TrimSpace(out) != "wait: draft" {
		t.Fatalf("draft: %d %q", code, out)
	}
	code, out, _ = h.run(me, "", "ready", "--json", me.label)
	if code != 2 || !strings.Contains(out, `"reason": "wrong_program"`) {
		t.Fatalf("plain pane: %d %q", code, out)
	}
}

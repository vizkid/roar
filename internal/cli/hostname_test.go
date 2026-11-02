package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTailnetCandidate(t *testing.T) {
	for _, c := range []struct{ dns, local, label, want string }{
		{"colonelk-rex.tail1.ts.net", "colonelk", "Baroni", "baroni-rex.tail1.ts.net"},
		{"stiletto-rex.tail1.ts.net.", "Stiletto", "baroni", "baroni-rex.tail1.ts.net"},
		{"studio.tail1.ts.net", "studio", "Baroni", "baroni.tail1.ts.net"}, // no suffix to copy
		{"other.tail1.ts.net", "studio", "Baroni", "baroni.tail1.ts.net"},
		{"", "studio", "Baroni", ""}, // Tailscale off
		{"nodot", "studio", "Baroni", ""},
	} {
		if got := tailnetCandidate(c.dns, c.local, c.label); got != c.want {
			t.Errorf("%q from %q: got %q, want %q", c.label, c.dns, got, c.want)
		}
	}
}

func stubLookup(t *testing.T, fn func(string) error) {
	old := lookupHost
	lookupHost = func(_ context.Context, name string) error { return fn(name) }
	t.Cleanup(func() { lookupHost = old })
}

// The field report: "Baroni:x" on a host with no stored hosts read as a pane
// label and failed as no_such_target, which looks like an unreachable host.
func TestUnknownHostPrefixSaysSo(t *testing.T) {
	stubLookup(t, func(string) error { return errors.New("no such host") })
	h := newHarness(t)
	sender := h.pane("sender")
	code, _, errs := h.run(sender, "", "send", "Nowhere:comm@stance", "hi")
	if code == 0 || !strings.Contains(errs, "unknown_host") || !strings.Contains(errs, "rex hosts add Nowhere") {
		t.Fatalf("%d %s", code, errs)
	}
}

// A label with no stored host resolves through its tailnet name when that
// name exists.
func TestSplitHostFallsBackToTailnetName(t *testing.T) {
	h := newHarness(t)
	var asked string
	stubLookup(t, func(name string) error { asked = name; return nil })
	host, rest, err := splitHost(t.Context(), h.rex, "Nowhere:comm@stance")
	if err != nil || rest != "comm@stance" {
		t.Fatalf("%q %q %v", host, rest, err)
	}
	if asked == "" {
		t.Skip("this Rex server is not on a tailnet")
	}
	if host != asked || !strings.HasPrefix(host, "nowhere") {
		t.Fatalf("host %q, looked up %q", host, asked)
	}
}

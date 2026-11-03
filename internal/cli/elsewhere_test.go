package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vizkid/roar/internal/rex"
)

// stubElsewhere makes hosts the other Rex hosts, where answer resolves a
// target.
func stubElsewhere(t *testing.T, hosts []string, answer func(host, target string) error) {
	oldHosts, oldResolve := otherHosts, resolveElsewhere
	otherHosts = func(context.Context, rex.Client) []string { return hosts }
	resolveElsewhere = func(_ context.Context, _ rex.Client, host, target string) error { return answer(host, target) }
	t.Cleanup(func() { otherHosts, resolveElsewhere = oldHosts, oldResolve })
}

// The field report: from Stiletto, `comm@stance` failed with no_such_target,
// because Stiletto has its own stance session and stance comm is on Baroni.
// The agent asked its user instead of trying Baroni.
func TestMissNamesTheHostThatHasIt(t *testing.T) {
	stubElsewhere(t, []string{"Baroni", "colonelk"}, func(host, _ string) error {
		if host == "Baroni" {
			return nil
		}
		return rex.ErrNoTarget
	})
	h := newHarness(t)
	sender := h.pane("sender")
	code, _, errs := h.run(sender, "", "send", "comm@nowhere", "hi")
	if code != exitRefused || !strings.Contains(errs, "no_such_target") || !strings.Contains(errs, "Baroni:comm@nowhere") {
		t.Fatalf("%d %s", code, errs)
	}
	if strings.Contains(errs, "colonelk") {
		t.Fatalf("named a host without the pane: %s", errs)
	}
}

// Roar names the other host but never sends there itself: the same label on
// two hosts may be two different agents.
func TestMissDoesNotSendElsewhere(t *testing.T) {
	stubElsewhere(t, []string{"Baroni"}, func(string, string) error { return nil })
	h := newHarness(t)
	sender := h.pane("sender")
	code, out, _ := h.run(sender, "", "send", "comm@nowhere", "hi")
	if code != exitRefused || out != "" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestElsewhere(t *testing.T) {
	miss := fmt.Errorf("%w: no match for pane %q", rex.ErrNoTarget, "comm")
	ambiguous := fmt.Errorf("%w: choose pane %q: a (comm-claude, claude), b (shell, codex)", rex.ErrAmbiguous, "comm")
	for _, c := range []struct {
		name    string
		hosts   []string
		answers map[string]error
		want    []string
		not     []string
	}{
		{"none elsewhere", []string{"Baroni"}, map[string]error{"Baroni": rex.ErrNoTarget}, nil, []string{"another host"}},
		{"no other hosts", nil, nil, nil, []string{"another host"}},
		// The other host has the session, but two panes match: the sender
		// needs the candidates to pick one.
		{"ambiguous there", []string{"Baroni"}, map[string]error{"Baroni": ambiguous}, []string{"Baroni:comm@stance", "comm-claude"}, nil},
		{"unreachable host", []string{"Baroni", "colonelk"}, map[string]error{"Baroni": nil, "colonelk": errors.New("timeout")}, []string{"Baroni:comm@stance"}, []string{"colonelk"}},
		{"two hosts", []string{"Baroni", "colonelk"}, map[string]error{"Baroni": nil, "colonelk": nil}, []string{"Baroni:comm@stance", "colonelk:comm@stance"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubElsewhere(t, c.hosts, func(host, _ string) error { return c.answers[host] })
			err := elsewhere(t.Context(), rex.Client{}, "comm@stance", miss)
			if !errors.Is(err, rex.ErrNoTarget) {
				t.Fatalf("lost no_such_target: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%v: missing %q", err, w)
				}
			}
			for _, n := range c.not {
				if strings.Contains(err.Error(), n) {
					t.Errorf("%v: has %q", err, n)
				}
			}
			if len(c.want) == 0 && err != miss {
				t.Errorf("changed a plain miss: %v", err)
			}
		})
	}
}

package rex_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/rextest"
)

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestResolveSinglePaneSession(t *testing.T) {
	c := rextest.Require(t)
	label := rextest.Label("one")
	session, block := rextest.Session(t, c, label, "sleep", "300")
	a, err := rex.Resolve(ctx(t), c, label)
	if err != nil {
		t.Fatal(err)
	}
	if a.Session != session || a.Block != block || a.Label != label || a.HostID == "" || a.Endpoint == "" {
		t.Fatalf("resolved %+v", a)
	}
}

func TestResolvePaneLabelAndBlockID(t *testing.T) {
	c := rextest.Require(t)
	label := rextest.Label("panes")
	session, first := rextest.Session(t, c, label, "sleep", "300")
	second := rextest.Split(t, c, session, first, "worker", "sleep", "300")

	if _, err := rex.Resolve(ctx(t), c, label); !errors.Is(err, rex.ErrAmbiguous) {
		t.Fatalf("two plain panes: %v", err)
	}
	a, err := rex.Resolve(ctx(t), c, label+"/worker")
	if err != nil || a.Block != second {
		t.Fatalf("by pane label: %+v %v", a, err)
	}
	a, err = rex.Resolve(ctx(t), c, second)
	if err != nil || a.Block != second || a.Session != session || a.Label != label {
		t.Fatalf("by block id: %+v %v", a, err)
	}
}

func TestResolvePrefersTheOnlyAgentPane(t *testing.T) {
	c := rextest.Require(t)
	// An interpreter running a script named codex is how npm-installed Codex
	// appears; Rex reports the script path. (A shell script that execs
	// another program would show only that program.)
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	if err := os.WriteFile(fake, []byte("#!/usr/bin/python3\nimport time\ntime.sleep(300)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	label := rextest.Label("agent")
	session, shell := rextest.Session(t, c, label, "sleep", "300")
	agent := rextest.Split(t, c, session, shell, "", fake)
	time.Sleep(500 * time.Millisecond)
	a, err := rex.Resolve(ctx(t), c, label)
	if err != nil || a.Block != agent {
		t.Fatalf("agent pane: %+v %v (want %s)", a, err, agent)
	}
}

func TestResolveFailures(t *testing.T) {
	c := rextest.Require(t)
	if _, err := rex.Resolve(ctx(t), c, rextest.Label("missing")); !errors.Is(err, rex.ErrNoTarget) {
		t.Fatalf("missing: %v", err)
	}
	label := rextest.Label("twice")
	rextest.Session(t, c, label, "sleep", "300")
	rextest.Session(t, c, label, "sleep", "300")
	_, err := rex.Resolve(ctx(t), c, label)
	if !errors.Is(err, rex.ErrAmbiguous) || !strings.Contains(err.Error(), "session:") {
		t.Fatalf("duplicate labels: %v", err)
	}
	if _, err := rex.Resolve(ctx(t), c, label+"/nope"); err == nil {
		t.Fatal("unknown pane label resolved")
	}
}

// A name with no session behind it can be a window (tab) or pane label in
// any session: people see window labels in Rex, not session labels.
func TestResolveWindowAndPaneLabels(t *testing.T) {
	c := rextest.Require(t)
	label, win := rextest.Label("tabs"), rextest.Label("win")
	session, block := rextest.Session(t, c, label, "sleep", "300")
	if _, err := c.Run(ctx(t), "window", "rename", "-s", session, win); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{win, label + "/" + win, win + "@" + label} {
		a, err := rex.Resolve(ctx(t), c, target)
		if err != nil || a.Block != block || a.Session != session || a.Label != label {
			t.Fatalf("%s: %+v %v", target, a, err)
		}
	}
	pane := rextest.Label("pane")
	worker := rextest.Split(t, c, session, block, pane, "sleep", "300")
	if a, err := rex.Resolve(ctx(t), c, pane); err != nil || a.Block != worker {
		t.Fatalf("pane label: %+v %v", a, err)
	}

	// The same window label in two sessions names neither.
	other, _ := rextest.Session(t, c, rextest.Label("tabs2"), "sleep", "300")
	if _, err := c.Run(ctx(t), "window", "rename", "-s", other, win); err != nil {
		t.Fatal(err)
	}
	if _, err := rex.Resolve(ctx(t), c, win); !errors.Is(err, rex.ErrAmbiguous) || !strings.Contains(err.Error(), win+"@"+label) {
		t.Fatalf("label in two sessions: %v", err)
	}
}

// Without a matching label, an agent name or a place in the tab names a
// pane. Layout: a shell top-left, a pane labelled "right" below it, and
// Codex filling the right half.
func TestResolveAgentAndPositionHints(t *testing.T) {
	c := rextest.Require(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	if err := os.WriteFile(fake, []byte("#!/usr/bin/python3\nimport time\ntime.sleep(300)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	label := rextest.Label("hints")
	session, shell := rextest.Session(t, c, label, "sleep", "300")
	agent := rextest.Split(t, c, session, shell, "", fake)
	var out struct {
		BlockIDs []string `json:"block_ids"`
	}
	if err := c.JSON(ctx(t), &out, "split", "-s", session, "-b", shell, "--split=below", "--focus=false",
		"--keep-open", "--json", "--label", "right", "--", "sleep", "300"); err != nil || len(out.BlockIDs) == 0 {
		t.Fatalf("split below: %v", err)
	}
	below := out.BlockIDs[0]
	time.Sleep(500 * time.Millisecond)

	for target, want := range map[string]string{
		"codex@" + label:       agent,
		label + "/codex":       agent,
		"top-left@" + label:    shell,
		"bottom-left@" + label: below,
		"right@" + label:       below, // a label wins over a hint
		"top@" + label:         agent, // two panes; the only agent wins
	} {
		if a, err := rex.Resolve(ctx(t), c, target); err != nil || a.Block != want {
			t.Errorf("%s: %+v %v, want %s", target, a.Block, err, want)
		}
	}
	if _, err := rex.Resolve(ctx(t), c, "left@"+label); !errors.Is(err, rex.ErrAmbiguous) {
		t.Errorf("left names two shells: %v", err)
	}
	if _, err := rex.Resolve(ctx(t), c, "claude@"+label); !errors.Is(err, rex.ErrNoTarget) {
		t.Errorf("no claude pane: %v", err)
	}
}

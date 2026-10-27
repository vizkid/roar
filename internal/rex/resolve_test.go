package rex_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/ping/internal/rex"
	"github.com/vizkid/ping/internal/rextest"
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

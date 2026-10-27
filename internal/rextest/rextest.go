// Package rextest creates throwaway Rex sessions for tests. Every session is
// labelled roar-test-* and killed when the test ends; tests never touch other
// sessions.
package rextest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rex"
)

var seq atomic.Int64

// Require returns the local Rex client, or skips the test when no Rex server
// answers.
func Require(t *testing.T) rex.Client {
	t.Helper()
	c := rex.Local()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Whoami(ctx); err != nil {
		t.Skipf("no Rex server: %v", err)
	}
	return c
}

// Label returns a unique session label for this test run.
func Label(name string) string {
	return fmt.Sprintf("roar-test-%s-%d-%d", name, os.Getpid(), seq.Add(1))
}

// Session creates a session whose first pane runs cmd (a shell when empty)
// and returns the session and block IDs.
func Session(t *testing.T, c rex.Client, label string, cmd ...string) (session, block string) {
	t.Helper()
	args := []string{"new", label, "--keep-open", "--json"}
	if len(cmd) > 0 {
		args = append(append(args, "--"), cmd...)
	}
	var out struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	run(t, c, &out, args...)
	t.Cleanup(func() { exec.Command("rex", "kill", out.SessionID).Run() })
	if len(out.InitialWindows) == 0 || len(out.InitialWindows[0].BlockIDs) == 0 {
		t.Fatalf("rex new returned no block: %+v", out)
	}
	return out.SessionID, out.InitialWindows[0].BlockIDs[0]
}

// Split adds a labelled pane beside block and returns its ID.
func Split(t *testing.T, c rex.Client, session, block, label string, cmd ...string) string {
	t.Helper()
	args := []string{"split", "-s", session, "-b", block, "--focus=false", "--keep-open", "--json"}
	if label != "" {
		args = append(args, "--label", label)
	}
	if len(cmd) > 0 {
		args = append(append(args, "--"), cmd...)
	}
	var out struct {
		BlockIDs []string `json:"block_ids"`
	}
	run(t, c, &out, args...)
	if len(out.BlockIDs) == 0 {
		t.Fatalf("rex split returned no block")
	}
	return out.BlockIDs[0]
}

func run(t *testing.T, c rex.Client, v any, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := c.Run(ctx, args...)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, v); err != nil {
		t.Fatalf("decoding %s: %v: %s", args[0], err, out)
	}
}

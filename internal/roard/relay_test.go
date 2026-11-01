package roard

import (
	"context"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/rextest"
)

func sessionBlocks(t *testing.T, c *rexctl.Client, sid string) []string {
	var out struct {
		Blocks []struct {
			BlockID   string `json:"block_id"`
			Placement string `json:"placement"`
		} `json:"blocks"`
	}
	if err := c.Call(t.Context(), "session.list_blocks", map[string]string{"session_id": sid}, &out); err != nil {
		return nil // destroyed
	}
	var ids []string
	for _, b := range out.Blocks {
		ids = append(ids, b.Placement+" "+b.BlockID)
	}
	return ids
}

// 58: roard closes the relay's idle shell, keeps the relay while a detached
// block runs, and destroys it once that block closes. No timer: the close
// arrives as an event.
func TestRetiresRelay(t *testing.T) {
	h := newHarness(t, "watcher.gap_recheck=1h")
	rc := rextest.Require(t)
	h.deps.RelayLabel = rextest.Label("relay")
	h.deps.Foreground = func(ctx context.Context, session, block string) (string, error) {
		p, err := rc.Process(ctx, session, block)
		if err != nil || p.Foreground == nil {
			return "", err
		}
		return p.Foreground.Name, nil
	}
	sid, _ := rextest.Session(t, rc, h.deps.RelayLabel) // a shell window, as rex new makes
	c := h.sender(nil)
	// Rex lets a connection change only sessions it is attached to.
	if err := c.Call(t.Context(), "session.attach_all", map[string]string{"event_scope": "session"}, nil); err != nil {
		t.Fatal(err)
	}
	var created struct {
		BlockID string `json:"block_id"`
	}
	err := c.Call(t.Context(), "session.new_block", map[string]any{"session_id": sid, "flavor": "com.superlogical.terminal.shell",
		"label": "roar-watch", "options": map[string]any{"shell": "none", "login": false, "command": []string{"sleep", "60"},
			"exit": map[string]any{"on_completion": true}}}, &created)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // let the shell start
	h.start()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b := sessionBlocks(t, c, sid)
		if len(b) == 1 && b[0] == "detached "+created.BlockID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocks %v: want only the detached one", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := c.Call(t.Context(), "block.close", map[string]string{"session_id": sid, "block_id": created.BlockID}, nil); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for sessionBlocks(t, c, sid) != nil {
		if time.Now().After(deadline) {
			t.Fatal("empty relay not destroyed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

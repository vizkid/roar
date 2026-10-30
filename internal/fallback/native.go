package fallback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vizkid/roar/internal/agy"
	"github.com/vizkid/roar/internal/codex"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/store"
)

// codex delivers through the pane's Codex daemon while its binding is live:
// Rex still reports the bound Codex TUI, alive, in the foreground. It returns
// false when it sent nothing, so typing may follow.
func (d *Deliverer) codex(ctx context.Context, c *store.Claim) (bool, error) {
	to := c.Message().Envelope.To
	b, ok := d.Store.CodexBinding(to.Block)
	if !ok {
		return false, nil
	}
	if !d.Rex.Runs(ctx, to.Session, to.Block, b.PID, "codex") {
		return false, nil
	}
	writer := fmt.Sprintf("codex:%d:%d", os.Getpid(), time.Now().UnixNano())
	return codex.Deliver(ctx, c, b, writer, d.Now)
}

// agy delivers through Antigravity's agent API while the pane's binding is
// live. A binding whose agy is gone, or whose token agy refused, is deleted:
// its token cannot work again. It returns false when it sent nothing.
func (d *Deliverer) agy(ctx context.Context, c *store.Claim) (bool, error) {
	to := c.Message().Envelope.To
	b, ok := d.Store.AgyBinding(to.Block)
	if !ok {
		return false, nil
	}
	p, err := d.Rex.Process(ctx, to.Session, to.Block)
	if errors.Is(err, rex.ErrNotFound) {
		d.Store.UnbindAgy(to.Block) // the pane is gone
	}
	if err != nil {
		return false, nil // the proof reports it
	}
	if p.LastExit != nil || p.Foreground == nil || p.Foreground.PID != b.PID || p.Foreground.Agent() != "agy" {
		d.Store.UnbindAgy(to.Block)
		return false, nil
	}
	st, err := d.Rex.ProgramStatus(ctx, to.Session, to.Block)
	if err != nil || st == nil || st.Owner == nil || st.Owner.PID != b.PID {
		return false, nil // the proof waits for a fresh status
	}
	var boundary string
	switch st.State {
	case "idle", "done":
		boundary = "turn"
	case "working":
		boundary = "mid_turn"
	default:
		// A message sent behind a permission prompt could read as its answer.
		_, err := d.hold(c, "modal")
		return true, err
	}
	writer := fmt.Sprintf("agy:%d:%d", os.Getpid(), time.Now().UnixNano())
	sent, err := agy.Deliver(ctx, c, b, boundary, writer, d.Now)
	if !sent && err == nil {
		d.Store.UnbindAgy(to.Block)
	}
	return sent, err
}

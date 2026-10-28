package fallback

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/vizkid/roar/internal/codex"
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

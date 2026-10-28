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
// Rex still reports the TUI that ran the hook in the foreground. It returns
// false when it sent nothing, so typing may follow.
func (d *Deliverer) codex(ctx context.Context, c *store.Claim) (bool, error) {
	to := c.Message().Envelope.To
	b, ok := d.Store.CodexBinding(to.Block)
	if !ok {
		return false, nil
	}
	p, err := d.Rex.Process(ctx, to.Session, to.Block)
	if err != nil || p.Foreground == nil || p.Foreground.PID != b.PID {
		return false, nil
	}
	writer := fmt.Sprintf("codex:%d:%d", os.Getpid(), time.Now().UnixNano())
	return codex.Deliver(ctx, c, b, writer, d.Now)
}

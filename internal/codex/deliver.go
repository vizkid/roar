package codex

import (
	"context"
	"errors"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// DeliverTimeout bounds one delivery, from dial to the turn call's result.
const DeliverTimeout = 15 * time.Second

// Deliver sends one claimed, queued message to the bound thread. It returns
// false when it sent nothing and left the message queued, so the caller may
// fall back: no daemon, or a thread the daemon has not loaded.
func Deliver(ctx context.Context, c *store.Claim, b store.CodexBinding, writer string, now func() time.Time) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, DeliverTimeout)
	defer cancel()
	cl, err := dial(ctx, Socket(b.CodexHome))
	if err != nil {
		return false, nil
	}
	defer cl.close()
	status, err := cl.status(ctx, b.Thread)
	if err != nil || (status != "idle" && status != "active") {
		return false, nil
	}
	// Record the write first: a crash after it must not look queued.
	t := now()
	if err := c.Move(store.Written, t, func(r *store.Record) { r.Writer, r.WrittenAt = writer, t }); err != nil {
		return true, err
	}
	text := c.Message().Render()
	method, err := cl.deliver(ctx, b.Thread, status, text)
	if errors.Is(err, errStale) {
		// The turn ended or began since the read. Nothing was sent; read again.
		if s, serr := cl.status(ctx, b.Thread); serr == nil && (s == "idle" || s == "active") {
			method, err = cl.deliver(ctx, b.Thread, s, text)
		}
	}
	var rerr *rpcError
	switch {
	case err == nil:
		proof, boundary := "codex_turn_start", "turn"
		if method == "turn/steer" {
			proof, boundary = "codex_turn_steer", "mid_turn"
		}
		return true, c.Move(store.Read, now(), func(r *store.Record) { r.Proof, r.Boundary = proof, boundary })
	case errors.Is(err, errStale), errors.As(err, &rerr):
		return true, c.Move(store.Queued, now(), func(r *store.Record) { r.HoldReason, r.Reason = "codex_rejected", err.Error() })
	default:
		// The call may have reached Codex: a turn runs on after its client
		// disconnects. Never send it again.
		return true, c.Move(store.Unknown, now(), func(r *store.Record) { r.Reason = err.Error() })
	}
}

// Reachable reports whether Deliver would use the binding now, and if not,
// why: no daemon socket, or a thread the daemon has not loaded. It sends
// nothing.
func Reachable(ctx context.Context, b store.CodexBinding) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, DeliverTimeout)
	defer cancel()
	cl, err := dial(ctx, Socket(b.CodexHome))
	if err != nil {
		return false, "no_codex_daemon"
	}
	defer cl.close()
	status, err := cl.status(ctx, b.Thread)
	switch {
	case err != nil:
		return false, "codex_thread_unreadable"
	case status != "idle" && status != "active":
		return false, "codex_thread_" + status
	}
	return true, ""
}

// Package agy delivers to Antigravity CLI through its local agent API:
// `agy agentapi send-message`, with the credentials a bound pane recorded.
package agy

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// DeliverTimeout bounds one `agentapi send-message` call.
const DeliverTimeout = 15 * time.Second

// Deliver sends one claimed, queued message to the bound conversation.
// boundary is "turn" for an agy at rest and "mid_turn" for a working one.
// It returns false when agy refused the call: nothing was sent, the message
// is queued again, and the caller may type it instead.
func Deliver(ctx context.Context, c *store.Claim, b store.AgyBinding, boundary, writer string, now func() time.Time) (bool, error) {
	// Record the write first: a crash after it must not look queued.
	t := now()
	if err := c.Move(store.Written, t, func(r *store.Record) { r.Writer, r.WrittenAt = writer, t }); err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(ctx, DeliverTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.Exe, "agentapi", "send-message", b.Conversation, c.Message().Render())
	cmd.Env = append(withoutAgy(os.Environ()), "ANTIGRAVITY_LS_ADDRESS="+b.Address, "ANTIGRAVITY_CSRF_TOKEN="+b.Token)
	var buf strings.Builder
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		// agy never ran, so nothing was sent.
		return false, c.Move(store.Queued, now(), func(r *store.Record) { r.Writer, r.Reason = "", "agy_start: "+err.Error() })
	}
	err := cmd.Wait()
	out := []byte(buf.String())
	var reply struct {
		Response struct {
			SendMessage json.RawMessage `json:"sendMessage"`
		} `json:"response"`
		Error string `json:"error"`
	}
	parsed := ctx.Err() == nil && json.Unmarshal(out, &reply) == nil
	switch {
	case parsed && len(reply.Response.SendMessage) > 0 && string(reply.Response.SendMessage) != "null":
		return true, c.Move(store.Read, now(), func(r *store.Record) { r.Proof, r.Boundary = "agy_send_message", boundary })
	case parsed && reply.Error != "":
		// agy answered and sent nothing, as for a stale token.
		return false, c.Move(store.Queued, now(), func(r *store.Record) { r.Writer, r.Reason = "", reply.Error })
	default:
		// The call may have reached agy. Never send it again.
		reason := "agy_reply_unreadable"
		if ctx.Err() != nil {
			reason = "agy_timeout"
		} else if err != nil && len(out) == 0 {
			reason = "agy_failed: " + err.Error()
		}
		return true, c.Move(store.Unknown, now(), func(r *store.Record) { r.Reason = reason })
	}
}

// withoutAgy drops any agy credentials Roar itself inherited, so the call
// carries only the binding's.
func withoutAgy(env []string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "ANTIGRAVITY_") {
			out = append(out, kv)
		}
	}
	return out
}

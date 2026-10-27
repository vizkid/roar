// Package fallback delivers messages by typing into a Rex pane, but only
// after the readiness proof says the agent's composer is empty.
package fallback

import (
	"context"
	"errors"
	"time"

	"github.com/vizkid/roar/internal/ready"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/store"
)

// SubmitWait is how long to wait for the agent to start a turn after Enter.
const SubmitWait = 5 * time.Second

// Deliverer runs fallback attempts against one host's store and Rex server.
type Deliverer struct {
	Store *store.Store
	Rex   rex.Client
	Now   func() time.Time
}

// Outcome is where an attempt left a message.
type Outcome struct {
	ID     string      `json:"id"`
	State  store.State `json:"state"`
	Reason string      `json:"reason,omitempty"`
}

// Attempt tries to deliver one queued message by typing. A message another
// process is handling, or one not queued, is left alone.
func (d *Deliverer) Attempt(ctx context.Context, id string) (Outcome, error) {
	c, err := d.Store.Claim(id)
	if errors.Is(err, store.ErrBusy) {
		return Outcome{ID: id, State: store.Queued, Reason: "claimed_elsewhere"}, nil
	}
	if err != nil {
		return Outcome{ID: id}, err
	}
	defer c.Release()
	m := c.Message()
	if m.Record.State != store.Queued {
		return outcome(m), nil
	}
	to := m.Envelope.To
	if d.Store.AdapterLive(to.Block) {
		return d.hold(c, "native_adapter")
	}
	r := ready.Probe(ctx, d.Rex, to.Session, to.Block, 0)
	switch {
	case r.Verdict == ready.Refuse && r.Reason == "dead_pane":
		err := c.Move(store.Expired, d.Now(), func(rec *store.Record) { rec.Reason = r.Reason })
		return outcome(c.Message()), err
	case r.Verdict != ready.Empty:
		// A pane running something else may start an agent later, and its
		// user can still read the message with `roar inbox`.
		return d.hold(c, r.Reason)
	}
	return d.typeMessage(ctx, c, r.PID)
}

func (d *Deliverer) hold(c *store.Claim, reason string) (Outcome, error) {
	m := c.Message()
	if m.Record.HoldReason != reason {
		if err := c.Move(store.Queued, d.Now(), func(r *store.Record) { r.HoldReason = reason }); err != nil {
			return outcome(m), err
		}
	}
	return outcome(c.Message()), nil
}

// typeMessage pastes the message, re-checks the agent, presses Enter, and
// waits for the agent to start a turn. Once text is pasted, any surprise
// ends in unknown: the composer may now hold our text, and nothing may
// type again until an operator settles it.
func (d *Deliverer) typeMessage(ctx context.Context, c *store.Claim, pid int) (Outcome, error) {
	m := c.Message()
	to := m.Envelope.To
	err := c.Move(store.Submitting, d.Now(), func(r *store.Record) {
		r.ForegroundPID = pid
		r.Attempts++
	})
	if err != nil {
		return outcome(m), err
	}
	unknown := func(reason string) (Outcome, error) {
		err := c.Move(store.Unknown, d.Now(), func(r *store.Record) { r.Reason = reason })
		return outcome(c.Message()), err
	}
	if err := d.Rex.Paste(ctx, to.Session, to.Block, m.Render()); err != nil {
		return unknown("paste_failed")
	}
	if r := ready.Status(ctx, d.Rex, to.Session, to.Block, pid); r.Verdict != ready.Empty {
		return unknown("changed_before_enter")
	}
	enterAt := time.Now()
	if err := d.Rex.SendKey(ctx, to.Session, to.Block, "enter"); err != nil {
		return unknown("enter_failed")
	}
	if !d.turnStarted(ctx, to.Session, to.Block, pid, enterAt) {
		return unknown("no_turn_started")
	}
	err = c.Move(store.Submitted, d.Now(), func(r *store.Record) { r.Proof = "rex_submit" })
	return outcome(c.Message()), err
}

// turnStarted waits for a status report from the same agent, made after
// Enter, that shows a turn: working, or already done or blocked.
func (d *Deliverer) turnStarted(ctx context.Context, session, block string, pid int, after time.Time) bool {
	deadline := time.Now().Add(SubmitWait)
	for time.Now().Before(deadline) {
		st, err := d.Rex.ProgramStatus(ctx, session, block)
		if err == nil && st != nil && st.Owner != nil && st.Owner.PID == pid {
			if st.State == "working" || (st.UpdatedAt.After(after) && st.State != "idle") {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

func outcome(m store.Message) Outcome {
	reason := m.Record.HoldReason
	if reason == "" {
		reason = m.Record.Reason
	}
	return Outcome{ID: m.Envelope.ID, State: m.Record.State, Reason: reason}
}

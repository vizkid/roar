package fallback

import (
	"context"
	"time"

	"github.com/vizkid/roar/internal/ready"
	"github.com/vizkid/roar/internal/rex"
)

// PasteWait is how long a mid-turn paste may take to show in the composer.
const PasteWait = 2 * time.Second

// pasteShown waits for the composer to hold the message whose header
// starts with head. A mid-turn submit is proved by the composer emptying,
// which proves nothing unless our text was there first.
func (d *Deliverer) pasteShown(ctx context.Context, session, block, agent, head string) bool {
	return poll(ctx, PasteWait, func() bool {
		return ready.Holds(ctx, d.Rex, session, block, agent, head)
	})
}

// submitted waits for proof that Enter delivered the text and names it:
// rex_submit for a report of a new turn, or, mid-turn, rex_steer for an
// emptied composer while the same Codex still reports its turn. Codex
// sends no new report when input joins a running turn. It returns "" when
// no proof came.
func (d *Deliverer) submitted(ctx context.Context, session, block string, pid int, agent string, after time.Time, midTurn bool) string {
	proof := ""
	poll(ctx, SubmitWait, func() bool {
		st, err := d.Rex.ProgramStatus(ctx, session, block)
		if err != nil {
			return false
		}
		if turnEvidence(st, pid, agent, after) {
			proof = "rex_submit"
		} else if midTurn && turnRunning(st, pid, agent) {
			if ready.Composer(ctx, d.Rex, session, block, agent) == "empty" {
				proof = "rex_steer"
			}
		}
		return proof != ""
	})
	return proof
}

// turnEvidence accepts a report made after Enter by the same agent process
// that shows a turn: working, or a fast turn already done or blocked. A
// report from before Enter could be a turn that began on its own.
func turnEvidence(st *rex.StatusRecord, pid int, agent string, after time.Time) bool {
	if !ownRecord(st, pid, agent) {
		return false
	}
	switch st.State {
	case "working", "done", "blocked":
		return st.UpdatedAt.After(after)
	}
	return false
}

// turnRunning accepts the same agent's record of the turn the text joined,
// or of that turn's end.
func turnRunning(st *rex.StatusRecord, pid int, agent string) bool {
	return ownRecord(st, pid, agent) && (st.State == "working" || st.State == "done")
}

func ownRecord(st *rex.StatusRecord, pid int, agent string) bool {
	return st != nil && st.Owner != nil && st.Owner.PID == pid && st.EffectiveApp == ready.App(agent)
}

// poll calls check every 100 ms until it returns true or wait passes.
func poll(ctx context.Context, wait time.Duration, check func() bool) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

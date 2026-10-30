// Package ready decides whether a Rex pane holds an agent with an empty
// composer, so terminal input cannot land in a draft, a shell, or a modal.
package ready

import (
	"github.com/vizkid/roar/internal/rex"
)

// Verdict is the outcome of the proof.
type Verdict int

const (
	Empty  Verdict = iota // safe to type
	Wait                  // not now; try again later
	Refuse                // never type into this pane
)

func (v Verdict) String() string {
	return [...]string{"empty", "wait", "refuse"}[v]
}

// Result explains a verdict. Agent and PID name the process proved ready.
type Result struct {
	Verdict Verdict `json:"-"`
	State   string  `json:"verdict"`
	Reason  string  `json:"reason"`
	Agent   string  `json:"agent,omitempty"`
	PID     int     `json:"pid,omitempty"`
}

// MidTurn is the reason for an empty composer during a turn, for an agent
// that adds input typed then to the running turn. Claude would queue it, so
// a Claude turn still waits.
const MidTurn = "mid_turn"

// steerable agents take typed input into a running turn, as shown live.
var steerable = map[string]bool{"codex": true, "devin": true}

// MidTurn reports an empty verdict for a pane whose turn is running.
func (r Result) MidTurn() bool { return r.Verdict == Empty && r.Reason == MidTurn }

func result(v Verdict, reason string) Result {
	return Result{Verdict: v, State: v.String(), Reason: reason}
}

// Snapshot is every Rex reading the proof needs, taken together.
type Snapshot struct {
	Process       rex.Process
	Status        *rex.StatusRecord
	CursorVisible bool
	Rows          int
	VT            string
}

var agentApps = map[string]string{"claude": "claude-code", "codex": "codex", "devin": "devin", "agy": "antigravity"}

// marker reports a composer prompt: Claude's, Codex's or Devin's, or for
// Antigravity its ASCII ">". That one counts only in an agy pane, because a
// quoted line in another agent's draft also starts with "> ".
func marker(r rune, agent string) bool {
	return r == '❯' || r == '›' || r == '❭' || (r == '>' && agent == "agy")
}

// App is the program status app an agent reports as.
func App(agent string) string { return agentApps[agent] }

// Decide applies the proof. wantPID, when not zero, is the agent process an
// earlier proof bound to; a different process means the pane changed hands.
// The first verdict wins, in the order of the spec.
func Decide(s Snapshot, wantPID int) Result {
	gate := decideProcessAndStatus(s, wantPID)
	if gate.Verdict != Empty {
		return gate
	}
	if !s.CursorVisible {
		return result(Wait, "cursor_hidden")
	}
	sc, err := parseScreen(s.VT, s.Rows)
	if err != nil {
		return result(Wait, "no_cursor")
	}
	if r := composer(sc, gate.Agent); r != "" {
		return result(Wait, r)
	}
	if gate.MidTurn() {
		return gate
	}
	gate.Reason = "empty"
	return gate
}

// decideProcessAndStatus applies steps 1 to 4 of the proof: the pane is
// alive, runs the expected agent, and that agent reports itself at rest.
func decideProcessAndStatus(s Snapshot, wantPID int) Result {
	fg := s.Process.Foreground
	if s.Process.LastExit != nil || fg == nil {
		return result(Refuse, "dead_pane")
	}
	agent := fg.Agent()
	if agent == "" {
		return result(Refuse, "wrong_program")
	}
	if wantPID != 0 && fg.PID != wantPID {
		return result(Refuse, "owner_changed")
	}
	st := s.Status
	switch {
	case st == nil:
		return result(Wait, "no_status")
	case st.Owner == nil || st.Owner.PID != fg.PID || st.Owner.Name != fg.Name:
		return result(Wait, "stale_status")
	case st.EffectiveApp != agentApps[agent]:
		return result(Wait, "foreign_status")
	case st.State == "working" && !steerable[agent]:
		return result(Wait, "busy")
	case st.State == "working":
		res := result(Empty, MidTurn)
		res.Agent, res.PID = agent, fg.PID
		return res
	case st.State != "idle" && st.State != "done":
		return result(Wait, "modal")
	}
	res := result(Empty, "at_rest")
	res.Agent, res.PID = agent, fg.PID
	return res
}

// drafted reports typed text in the composer. The cursor may sit on a
// continuation row of a long draft, so it looks up for the marker row.
func drafted(sc screen, agent string) bool {
	for i := sc.cursorRow - 1; i >= 0 && i < len(sc.rows); i-- {
		r := sc.rows[i]
		if r.isRule() {
			return false
		}
		if len(r.cells) >= 1 && marker(r.cells[0].r, agent) {
			return r.typedText(2, agent) || i < sc.cursorRow-1
		}
		if !r.typedText(0, agent) {
			return false
		}
	}
	return false
}

// composer checks the cursor row. It returns "" for an empty composer.
// It never reads composer text to judge whose draft it is; it only asks
// whether any typed (not dim) text exists.
func composer(sc screen, agent string) string {
	i := sc.cursorRow - 1
	if i < 0 || i >= len(sc.rows) {
		return "no_marker"
	}
	r := sc.rows[i]
	// The marker is followed by a space, or ends the row: a fresh agy trims
	// the blank cells after an empty marker.
	if len(r.cells) < 1 || !marker(r.cells[0].r, agent) ||
		(len(r.cells) > 1 && r.cells[1].r != ' ' && r.cells[1].r != ' ') {
		return "no_marker"
	}
	if sc.cursorCol != 3 || r.typedText(2, agent) {
		return "draft"
	}
	for _, next := range sc.rows[i+1:] {
		if next.isRule() || next.blank() {
			break
		}
		if next.typedText(0, agent) {
			return "draft"
		}
	}
	return ""
}

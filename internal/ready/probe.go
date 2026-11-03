package ready

import (
	"context"
	"errors"
	"time"

	"github.com/vizkid/rexkit/rex"
)

// Freshness bounds how long one probe may take. Older readings could
// describe a pane that has since changed.
const Freshness = 2 * time.Second

// Probe takes the readings for one pane and decides. Each reading is taken
// only when the gates before it passed: a dead pane costs one Rex call, a
// busy or modal agent two, and only an agent at rest has its screen read.
func Probe(ctx context.Context, c rex.Client, session, block string, wantPID int) Result {
	r, _ := ProbeProcess(ctx, c, session, block, wantPID)
	return r
}

// ProbeProcess is Probe that also returns its process reading, so a caller
// that needs the process does not read it again. The process is zero when
// that read failed.
func ProbeProcess(ctx context.Context, c rex.Client, session, block string, wantPID int) (Result, rex.Process) {
	start := time.Now()
	var s Snapshot
	var err error
	fresh := func(r Result) (Result, rex.Process) {
		if time.Since(start) > Freshness {
			r = result(Wait, "stale_probe")
		}
		return r, s.Process
	}
	if s.Process, err = c.Process(ctx, session, block); errors.Is(err, rex.ErrNotFound) {
		return result(Refuse, "dead_pane"), s.Process
	} else if err != nil {
		return result(Wait, "probe_failed"), s.Process
	}
	if s.Process.Foreground == nil || s.Process.LastExit != nil {
		return fresh(decideProcessAndStatus(s, wantPID))
	}
	if s.Status, err = c.ProgramStatus(ctx, session, block); err != nil {
		return fresh(result(Wait, "probe_failed"))
	}
	if gate := decideProcessAndStatus(s, wantPID); gate.Verdict != Empty {
		return fresh(gate)
	}
	if s.CursorVisible, err = c.CursorVisible(ctx, session, block); err != nil {
		return fresh(result(Wait, "probe_failed"))
	}
	if s.CursorVisible { // a hidden cursor decides without the screen
		if s.Rows, err = c.Rows(ctx, session, block); err == nil {
			s.VT, err = c.ScreenVT(ctx, session, block)
		}
		if err != nil {
			return fresh(result(Wait, "probe_failed"))
		}
	}
	return fresh(Decide(s, wantPID))
}

// Status re-runs only the process and status gates. The fallback uses it
// between paste and Enter, when the composer holds our own text.
func Status(ctx context.Context, c rex.Client, session, block string, wantPID int) Result {
	start := time.Now()
	var s Snapshot
	var err error
	if s.Process, err = c.Process(ctx, session, block); err != nil {
		return result(Refuse, "dead_pane")
	}
	if s.Status, err = c.ProgramStatus(ctx, session, block); err != nil {
		return result(Wait, "probe_failed")
	}
	if time.Since(start) > Freshness {
		return result(Wait, "stale_probe")
	}
	return decideProcessAndStatus(s, wantPID)
}

// Composer reads the screen and says what agent's composer holds: "empty",
// "draft", or "" when the screen shows no composer.
func Composer(ctx context.Context, c rex.Client, session, block, agent string) string {
	sc, ok := readScreen(ctx, c, session, block)
	switch {
	case !ok:
		return ""
	case composer(sc, agent) == "":
		return "empty"
	case drafted(sc, agent):
		return "draft"
	}
	return ""
}

// Holds reports whether the composer holds the pasted text whose first
// line starts with head, or, for a paste the agent folded into one
// placeholder, any typed text at all.
func Holds(ctx context.Context, c rex.Client, session, block, agent, head string) bool {
	sc, ok := readScreen(ctx, c, session, block)
	return ok && (holds(sc, agent, head) || drafted(sc, agent))
}

// readScreen reads the screen while the cursor shows; a hidden cursor
// means no composer has focus.
func readScreen(ctx context.Context, c rex.Client, session, block string) (screen, bool) {
	visible, err := c.CursorVisible(ctx, session, block)
	if err != nil || !visible {
		return screen{}, false
	}
	rows, err := c.Rows(ctx, session, block)
	if err != nil {
		return screen{}, false
	}
	vt, err := c.ScreenVT(ctx, session, block)
	if err != nil {
		return screen{}, false
	}
	sc, err := parseScreen(vt, rows)
	return sc, err == nil
}

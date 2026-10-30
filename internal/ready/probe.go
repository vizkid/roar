package ready

import (
	"context"
	"errors"
	"time"

	"github.com/vizkid/roar/internal/rex"
)

// Freshness bounds how long one probe may take. Older readings could
// describe a pane that has since changed.
const Freshness = 2 * time.Second

// Probe takes every reading for one pane and decides. The process is read
// first, so a dead pane refuses without further calls.
func Probe(ctx context.Context, c rex.Client, session, block string, wantPID int) Result {
	start := time.Now()
	s, err := snapshot(ctx, c, session, block)
	if errors.Is(err, rex.ErrNotFound) {
		return result(Refuse, "dead_pane")
	}
	if err != nil {
		return result(Wait, "probe_failed")
	}
	if time.Since(start) > Freshness {
		return result(Wait, "stale_probe")
	}
	return Decide(s, wantPID)
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
	visible, err := c.CursorVisible(ctx, session, block)
	if err != nil || !visible {
		return ""
	}
	rows, err := c.Rows(ctx, session, block)
	if err != nil {
		return ""
	}
	vt, err := c.ScreenVT(ctx, session, block)
	if err != nil {
		return ""
	}
	sc, err := parseScreen(vt, rows)
	switch {
	case err != nil:
		return ""
	case composer(sc, agent) == "":
		return "empty"
	case drafted(sc, agent):
		return "draft"
	}
	return ""
}

func snapshot(ctx context.Context, c rex.Client, session, block string) (Snapshot, error) {
	var s Snapshot
	var err error
	if s.Process, err = c.Process(ctx, session, block); err != nil {
		return s, err
	}
	if s.Process.Foreground == nil || s.Process.LastExit != nil {
		return s, nil
	}
	if s.Status, err = c.ProgramStatus(ctx, session, block); err != nil {
		return s, err
	}
	if s.CursorVisible, err = c.CursorVisible(ctx, session, block); err != nil {
		return s, err
	}
	if s.Rows, err = c.Rows(ctx, session, block); err != nil {
		return s, err
	}
	s.VT, err = c.ScreenVT(ctx, session, block)
	return s, err
}

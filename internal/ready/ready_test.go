package ready

import (
	"os"
	"strings"
	"testing"

	"github.com/vizkid/roar/internal/rex"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".vt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var claudeProc = rex.Proc{PID: 100, Name: "2.1.311", InvokedPath: "/home/u/.local/bin/claude", Argv0: "/home/u/.local/bin/claude"}
var codexProc = rex.Proc{PID: 200, Name: "node", Path: "/usr/bin/node", Script: "/usr/lib/node_modules/@openai/codex/bin/codex"}
var devinProc = rex.Proc{PID: 300, Name: "devin", Path: "/home/u/.local/share/devin/cli/_versions/3000.11.92/bin/devin", Argv0: "devin"}

func status(state string, proc rex.Proc, app string) *rex.StatusRecord {
	r := &rex.StatusRecord{State: state, App: app, EffectiveApp: app}
	r.Owner = &struct {
		PID  int    `json:"pid"`
		Name string `json:"name"`
	}{proc.PID, proc.Name}
	return r
}

func snap(t *testing.T, agent, screen, state string) Snapshot {
	proc, app := claudeProc, "claude-code"
	switch agent {
	case "codex":
		proc, app = codexProc, "codex"
	case "devin":
		proc, app = devinProc, "devin"
	}
	p := proc
	return Snapshot{
		Process:       rex.Process{Foreground: &p},
		Status:        status(state, proc, app),
		CursorVisible: true,
		Rows:          24,
		VT:            fixture(t, agent+"-"+screen),
	}
}

func expect(t *testing.T, name string, got Result, v Verdict, reason string) {
	t.Helper()
	if got.Verdict != v || got.Reason != reason {
		t.Errorf("%s: got %s/%s, want %s/%s", name, got.Verdict, got.Reason, v, reason)
	}
}

func TestComposerStates(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "devin"} {
		expect(t, agent+" idle", Decide(snap(t, agent, "idle", "idle"), 0), Empty, "empty")
		expect(t, agent+" done", Decide(snap(t, agent, "idle", "done"), 0), Empty, "empty")
		expect(t, agent+" draft", Decide(snap(t, agent, "draft", "idle"), 0), Wait, "draft")
		// The cursor moved home passes a cursor-only check; the dim check catches it.
		expect(t, agent+" draft, cursor home", Decide(snap(t, agent, "home", "idle"), 0), Wait, "draft")
		expect(t, agent+" blocked", Decide(snap(t, agent, "idle", "blocked"), 0), Wait, "modal")
		expect(t, agent+" unknown status", Decide(snap(t, agent, "idle", "thinking"), 0), Wait, "modal")
	}
	// A mid-turn composer looks empty; only the status tells. Claude queues
	// mid-turn input, so it waits; Codex and Devin add it to the running turn.
	expect(t, "claude working", Decide(snap(t, "claude", "idle", "working"), 0), Wait, "busy")
	expect(t, "devin working", Decide(snap(t, "devin", "idle", "working"), 0), Empty, "mid_turn")
	expect(t, "devin working, draft", Decide(snap(t, "devin", "draft", "working"), 0), Wait, "draft")
	expect(t, "codex working", Decide(snap(t, "codex", "idle", "working"), 0), Empty, "mid_turn")
	expect(t, "codex working, draft", Decide(snap(t, "codex", "draft", "working"), 0), Wait, "draft")
	expect(t, "codex working, cursor home", Decide(snap(t, "codex", "home", "working"), 0), Wait, "draft")
}

func TestEmptyReportsAgentAndPID(t *testing.T) {
	r := Decide(snap(t, "codex", "idle", "idle"), 0)
	if r.Agent != "codex" || r.PID != 200 {
		t.Fatalf("got %+v", r)
	}
}

func TestProcessGates(t *testing.T) {
	s := snap(t, "claude", "idle", "idle")
	s.Process = rex.Process{LastExit: &rex.Exit{Code: 3}}
	expect(t, "exited", Decide(s, 0), Refuse, "dead_pane")

	s = snap(t, "claude", "idle", "idle")
	s.Process.Foreground = nil
	expect(t, "no foreground", Decide(s, 0), Refuse, "dead_pane")

	s = snap(t, "claude", "idle", "idle")
	s.Process.Foreground = &rex.Proc{PID: 7, Name: "-zsh", Path: "/bin/zsh"}
	expect(t, "shell", Decide(s, 0), Refuse, "wrong_program")

	s = snap(t, "claude", "idle", "idle")
	expect(t, "replaced agent", Decide(s, 999), Refuse, "owner_changed")
	expect(t, "same agent", Decide(s, 100), Empty, "empty")
}

func TestStatusGates(t *testing.T) {
	s := snap(t, "claude", "idle", "idle")
	s.Status = nil
	expect(t, "no status (startup, trust dialog)", Decide(s, 0), Wait, "no_status")

	// A killed agent leaves a stale idle record owned by the dead process.
	s = snap(t, "claude", "idle", "idle")
	s.Status.Owner.PID = 55
	expect(t, "stale record", Decide(s, 0), Wait, "stale_status")

	s = snap(t, "claude", "idle", "idle")
	s.Status.Owner.Name = "other"
	expect(t, "reused pid", Decide(s, 0), Wait, "stale_status")

	s = snap(t, "claude", "idle", "idle")
	s.Status.EffectiveApp, s.Status.App = "cargo", "cargo"
	expect(t, "foreign status", Decide(s, 0), Wait, "foreign_status")
}

func TestScreenGates(t *testing.T) {
	s := snap(t, "claude", "idle", "idle")
	s.CursorVisible = false
	expect(t, "hidden cursor", Decide(s, 0), Wait, "cursor_hidden")

	s = snap(t, "claude", "idle", "idle")
	s.VT = strings.TrimSuffix(s.VT, "\x1b[22;3H") + "\x1b[2;3H"
	expect(t, "cursor off the composer", Decide(s, 0), Wait, "no_marker")

	s = snap(t, "claude", "idle", "idle")
	s.VT = strings.TrimSuffix(s.VT, "\x1b[22;3H")
	expect(t, "no cursor report", Decide(s, 0), Wait, "no_cursor")

	// A second draft line below an empty-looking marker row.
	s = snap(t, "claude", "idle", "idle")
	s.VT = strings.Replace(s.VT, "\x1b[2mTry \"fix lint errors\"\x1b[0m\r\n", "\r\n  second line\r\n", 1)
	s.Rows = 25
	s.VT = strings.TrimSuffix(s.VT, "\x1b[22;3H") + "\x1b[22;3H"
	expect(t, "continuation row", Decide(s, 0), Wait, "draft")
}

func TestParseScreen(t *testing.T) {
	sc, err := parseScreen(fixture(t, "claude-draft"), 24)
	if err != nil {
		t.Fatal(err)
	}
	if sc.cursorRow != 22 || sc.cursorCol != 19 {
		t.Fatalf("cursor %d,%d", sc.cursorRow, sc.cursorCol)
	}
	row := sc.rows[sc.cursorRow-1]
	if row.text() != "❯ draft words here" {
		t.Fatalf("row %q", row.text())
	}
	idle, _ := parseScreen(fixture(t, "claude-idle"), 24)
	r := idle.rows[21]
	if r.cells[0].dim || !r.cells[2].dim {
		t.Fatalf("dim flags wrong: %+v", r.cells[:3])
	}
}

// A mid-turn submit needs proof that the paste reached the composer, also
// when a long draft puts the cursor on a continuation row.
func TestDrafted(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "devin"} {
		idle, _ := parseScreen(fixture(t, agent+"-idle"), 24)
		draft, _ := parseScreen(fixture(t, agent+"-draft"), 24)
		if drafted(idle, agent) || !drafted(draft, agent) {
			t.Errorf("%s: idle %v, draft %v", agent, drafted(idle, agent), drafted(draft, agent))
		}
		multi := draft
		multi.rows = append(append([]row{}, draft.rows[:draft.cursorRow]...), row{[]cell{{r: ' '}, {r: ' '}, {r: 'x'}}})
		multi.rows = append(multi.rows, draft.rows[draft.cursorRow:]...)
		multi.cursorRow, multi.cursorCol = draft.cursorRow+1, 4
		if !drafted(multi, agent) {
			t.Errorf("%s: continuation row not seen as a draft", agent)
		}
	}
}

// Devin draws its placeholder in grey, not dim. Only that grey counts as
// placeholder: any other colour, or none, is typed text.
func TestDevinPlaceholderColour(t *testing.T) {
	s := snap(t, "devin", "idle", "idle")
	s.VT = strings.Replace(s.VT, "38;2;124;124;124mAsk Devin", "38;2;200;200;200mAsk Devin", 1)
	expect(t, "other grey", Decide(s, 0), Wait, "draft")
	// The same grey from Claude is typed text: the rule is per agent.
	c := snap(t, "claude", "idle", "idle")
	c.VT = strings.Replace(c.VT, "\x1b[2mTry", "\x1b[38;2;124;124;124mTry", 1)
	expect(t, "claude with devin grey", Decide(c, 0), Wait, "draft")
}

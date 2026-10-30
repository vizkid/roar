package fallback

import (
	"testing"

	"github.com/vizkid/roar/internal/agenttest"
	"github.com/vizkid/roar/internal/store"
)

// Codex and Devin turns can run for tens of minutes. The composer stays
// live, and Enter adds the input to the running turn, with no new status
// report from Codex.
func TestSteersIntoBusyAgent(t *testing.T) {
	f := setup(t)
	for _, kind := range []string{"codex", "devin"} {
		a := agenttest.Start(t, f.rex, f.bin, kind)
		a.Control("working")
		e := f.queue(a.Session, a.Block, "first line\nsecond line")
		want(t, f.attempt(e.ID), store.Submitted, "")
		if got := a.Submitted(); len(got) != 1 || got[0] != e.Render() {
			t.Fatalf("%s received %q, want %q", kind, agenttest.Joined(got), e.Render())
		}
		if m, _ := f.st.Get(e.ID); m.Record.Proof != "rex_steer" {
			t.Fatalf("%s record %+v", kind, m.Record)
		}
	}
}

// Without its own text in the composer, Enter could submit nothing, and an
// empty composer afterwards would prove nothing.
func TestPasteNotSeenLeavesUnknown(t *testing.T) {
	f := setup(t)
	a := agenttest.Start(t, f.rex, f.bin, "codex", "TESTAGENT_DROP_PASTE=1")
	a.Control("working")
	e := f.queue(a.Session, a.Block, "hello")
	want(t, f.attempt(e.ID), store.Unknown, "paste_not_seen")
	if got := a.Submitted(); len(got) != 0 {
		t.Fatalf("submitted %q", agenttest.Joined(got))
	}
}

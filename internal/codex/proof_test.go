package codex

import (
	"context"
	"slices"
	"testing"
)

// Only a header that starts a user message counts: Roar writes it there,
// and a body quoting a header does not.
func TestDeliveredIDs(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "idle", "")
	d.inputs = []string{
		"[roar rm-01OLD from a/1 inform]\nfirst",
		"plain text from the operator",
		"see [roar rm-01QUOTED from a/1 inform]",
		"[roar rm-01NEW from a/1 request]\nsecond",
	}
	ids, err := DeliveredIDs(context.Background(), d.home, "T1")
	if err != nil || !slices.Equal(ids, []string{"rm-01NEW", "rm-01OLD"}) {
		t.Fatalf("ids %v, %v", ids, err)
	}
	if _, err := DeliveredIDs(context.Background(), "/nonexistent", "T1"); err == nil {
		t.Fatal("no daemon should be an error")
	}
}

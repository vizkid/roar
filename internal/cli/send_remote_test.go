package cli

import (
	"testing"
	"time"
)

// Rex takes an upload one Chunk per call. A large attachment needs more
// than the two minutes a plain message gets.
func TestSendBudgetGrowsWithAttachments(t *testing.T) {
	if got := sendBudget(nil); got != 2*time.Minute {
		t.Fatalf("plain message budget %s", got)
	}
	big := map[string][]byte{"part": make([]byte, 12<<20)}
	if got := sendBudget(big); got < 2*time.Minute+5*time.Minute {
		t.Fatalf("12 MiB budget %s: under a second per rex write", got)
	}
}

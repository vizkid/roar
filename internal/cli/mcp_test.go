package cli

import (
	"testing"
	"time"

	"github.com/vizkid/roar/internal/rex"
)

// The Claude adapter trusts only its own Claude's status record.
func TestClaudeStatus(t *testing.T) {
	at := time.Now()
	rec := func(app string, pid int) *rex.StatusRecord {
		r := &rex.StatusRecord{State: "idle", EffectiveApp: app, UpdatedAt: at}
		r.Owner = &struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		}{PID: pid}
		return r
	}
	if s, err := claudeStatus(rec("claude-code", 42), 42); err != nil || s.State != "idle" || !s.At.Equal(at) {
		t.Fatalf("own record: %+v %v", s, err)
	}
	for name, r := range map[string]*rex.StatusRecord{
		"no record":       nil,
		"another process": rec("claude-code", 43),
		"another app":     rec("codex", 42),
		"record no owner": {State: "idle", EffectiveApp: "claude-code"},
	} {
		if _, err := claudeStatus(r, 42); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

package roard

import (
	"encoding/json"

	"github.com/vizkid/rexkit/rexctl"
)

// unwrap returns the event a session_event carries. A session's events,
// block_event and block_closed among them, arrive inside one; server events
// such as client_left and session_destroyed arrive bare and pass through. A
// session_event that does not decode becomes an event no consumer knows.
func unwrap(e rexctl.Event) rexctl.Event {
	if e.Type != "session_event" {
		return e
	}
	var s struct {
		Event struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"event"`
	}
	if json.Unmarshal(e.Payload, &s) != nil {
		return rexctl.Event{Seq: e.Seq}
	}
	return rexctl.Event{Seq: e.Seq, Type: s.Event.Type, Payload: s.Event.Payload}
}

// relayTrigger reports whether an unwrapped event may have emptied the
// legacy relay session, so the relay check runs again.
func relayTrigger(e rexctl.Event) bool {
	return e.Type == "block_closed" || e.Type == "session_destroyed"
}

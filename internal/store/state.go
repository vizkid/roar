package store

import "time"

// State is where a message stands. Every change is fsynced before Ping acts on it.
type State string

const (
	Queued     State = "queued"
	Written    State = "written"    // a native adapter handed it to the provider
	Accepted   State = "accepted"   // Claude recorded it as enqueued
	Read       State = "read"       // provider receipt, or the recipient marked it
	Submitting State = "submitting" // the fallback began typing
	Submitted  State = "submitted"  // Enter sent and Rex reported the agent working
	Unknown    State = "unknown"    // an attempt ended without a result
	Expired    State = "expired"
)

var moves = map[State][]State{
	Queued:     {Written, Submitting, Read, Expired},
	Written:    {Accepted, Read, Queued, Unknown, Expired},
	Accepted:   {Read, Queued, Expired},
	Submitting: {Submitted, Unknown},
	// Only an operator's `ping resolve` leaves Unknown.
	Unknown: {Read, Queued},
}

// CanMove reports whether a message may go from one state to another.
func CanMove(from, to State) bool {
	for _, s := range moves[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Final reports whether no further delivery can happen.
func (s State) Final() bool {
	return s == Read || s == Submitted || s == Expired
}

// Record is the mutable half of a message, replaced atomically on every change.
type Record struct {
	State     State     `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
	// HoldReason explains why a queued message is waiting.
	HoldReason string `json:"hold_reason,omitempty"`
	// Reason explains an expired or unknown message.
	Reason string `json:"reason,omitempty"`
	// Proof and Boundary describe a read receipt.
	Proof    string `json:"proof,omitempty"`
	Boundary string `json:"boundary,omitempty"`
	// Writer identifies the adapter process that wrote the message.
	Writer    string    `json:"writer,omitempty"`
	WrittenAt time.Time `json:"written_at,omitzero"`
	// ForegroundPID is the agent process the fallback proved ready.
	ForegroundPID int `json:"foreground_pid,omitempty"`
	Attempts      int `json:"attempts,omitempty"`
}

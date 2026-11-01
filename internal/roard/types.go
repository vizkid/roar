// Package roard is Roar's one service per host. It connects to the local
// Rex server as a client and offers roar.* actions that other hosts call
// with client.perform, calls senders back with roar.result as their
// messages change, and runs the watcher. docs/ROARD.md is the design.
package roard

import (
	"context"
	"time"

	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/store"
)

// Protocol is the roar.* action protocol version that roar.hello reports.
const Protocol = 1

// Delivery attempts the oldest queued message of one pane.
type Delivery interface {
	Block(ctx context.Context, block string)
}

// Deps is everything roard needs from the rest of Roar.
type Deps struct {
	Store    *store.Store
	Settings func() *config.Settings // the current settings; a reload swaps them
	Endpoint string                  // the local Rex server
	HostID   string
	Version  string
	Label    string // the Rex client label; "roard" when empty
	Delivery Delivery
	Panes    func(ctx context.Context) (any, error)
	UID      int
	Logf     func(format string, args ...any)
}

func (d Deps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

// Row is one message's record as roard reports it.
type Row struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	HoldReason string    `json:"hold_reason,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Proof      string    `json:"proof,omitempty"`
	Boundary   string    `json:"boundary,omitempty"`
	Revision   int64     `json:"revision,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
}

func rowOf(m store.Message) Row {
	r := m.Record
	return Row{ID: m.Envelope.ID, State: string(r.State), HoldReason: r.HoldReason, Reason: r.Reason,
		Proof: r.Proof, Boundary: r.Boundary, Revision: r.Revision, UpdatedAt: r.UpdatedAt}
}

// missing is the row for an ID this host does not hold.
func missing(id string) Row { return Row{ID: id, State: "missing"} }

// Result is the argument of a roar.result call: a job's outcome, or a
// change to a subscribed message.
type Result struct {
	Job          string        `json:"job,omitempty"`
	Subscription string        `json:"subscription,omitempty"`
	Revision     int64         `json:"revision,omitempty"`
	Rows         []Row         `json:"rows,omitempty"`
	Panes        any           `json:"panes,omitempty"`
	Error        *rexctl.Error `json:"error,omitempty"`
}

// Hints are a sender's own needs for calls back to it, such as a slow link.
type Hints struct {
	CallbackTimeoutMS int64 `json:"callback_timeout_ms,omitempty"`
	CallbackGiveUpMS  int64 `json:"callback_give_up_ms,omitempty"`
}

// allowed is the caller check: Rex names every caller, and callers cannot
// fake it. A Tailscale caller that Rex admitted can already run programs as
// this account, so roard adds no trust Rex did not give.
func allowed(p rexctl.Principal, uid int) bool {
	switch p.Kind {
	case "peer":
		return p.UID != nil && *p.UID == uid
	case "tailscale":
		return true
	}
	return false
}

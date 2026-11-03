package roard

import (
	"context"
	"strings"
	"sync"

	"github.com/vizkid/rexkit/rexctl"
)

// RelayLabel is the session that older Roar kept on every host for its
// detached blocks. roard retires it.
const RelayLabel = "roar-relay"

// shells are foreground programs that mean a block is an idle shell.
var shells = map[string]bool{"zsh": true, "bash": true, "sh": true, "dash": true, "fish": true, "ksh": true, "tcsh": true, "nu": true, "login": true}

// relay retires roar-relay: it closes the idle shell that rex new gave the
// session, and destroys the session once no detached block remains. It
// checks when roard connects and when a block closes; no timer.
type relay struct {
	d   Deps
	mu  sync.Mutex
	sid string
}

func (r *relay) label() string {
	if r.d.RelayLabel != "" {
		return r.d.RelayLabel
	}
	return RelayLabel
}

// check runs on connect and after each block_closed or session_destroyed.
func (r *relay) check(ctx context.Context, c *rexctl.Client) {
	if !r.d.Settings().Bool("roard.relay.retire") || r.d.Foreground == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var ss struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Label     string `json:"label"`
		} `json:"sessions"`
	}
	if c.Call(ctx, "session.list", nil, &ss) != nil {
		return
	}
	r.sid = ""
	for _, s := range ss.Sessions {
		if s.Label == r.label() {
			r.sid = s.SessionID
		}
	}
	if r.sid == "" {
		return
	}
	var bl struct {
		Blocks []struct {
			BlockID   string `json:"block_id"`
			Placement string `json:"placement"`
		} `json:"blocks"`
	}
	if c.Call(ctx, "session.list_blocks", map[string]string{"session_id": r.sid}, &bl) != nil {
		return
	}
	busy := 0
	for _, b := range bl.Blocks {
		if b.Placement != "placed" {
			busy++ // an old watcher or a remote call: wait for it to close
			continue
		}
		fg, err := r.d.Foreground(ctx, r.sid, b.BlockID)
		if err != nil || !shells[strings.TrimPrefix(fg, "-")] && fg != "" {
			busy++ // something runs in it; leave it
			continue
		}
		if err := c.Call(ctx, "block.close", map[string]string{"session_id": r.sid, "block_id": b.BlockID}, nil); err == nil {
			r.d.logf("relay: closed its idle shell %s", b.BlockID)
		}
	}
	if busy == 0 {
		if err := c.Call(ctx, "session.destroy", map[string]string{"session_id": r.sid}, nil); err == nil {
			r.d.logf("relay: destroyed %s", r.label())
			r.sid = ""
		}
	}
}

// known reports whether a relay session was seen, so events are cheap
// once it is gone.
func (r *relay) known() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sid != ""
}

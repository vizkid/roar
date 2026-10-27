package cli

import (
	"context"
	"errors"

	"github.com/vizkid/roar/internal/envelope"
)

var errNotInPane = errors.New("not_in_rex_pane: run roar inside a Rex pane (REX_SESSION and REX_BLOCK unset)")

// self is the caller's own pane. It prevents mistakes, not attacks: any
// process can set these variables.
func self(ctx context.Context, env Env) (envelope.Address, error) {
	session, block := env.Getenv("REX_SESSION"), env.Getenv("REX_BLOCK")
	if session == "" || block == "" {
		return envelope.Address{}, errNotInPane
	}
	w, err := env.Rex.Whoami(ctx)
	if err != nil {
		return envelope.Address{}, err
	}
	a := envelope.Address{HostID: w.HostID, Endpoint: w.Endpoint, Session: session, Block: block}
	if u := w.Tailscale.HTTPS.URL; u != "" {
		a.Endpoint = u
	}
	if sessions, err := env.Rex.Sessions(ctx); err == nil {
		for _, s := range sessions {
			if s.ID == session {
				a.Label = s.Label
			}
		}
	}
	return a, nil
}

package cli

import (
	"context"
	"errors"

	"github.com/vizkid/roar/internal/envelope"
)

var errNotInPane = errors.New("not_in_rex_pane: run roar inside a Rex pane (REX_SESSION and REX_BLOCK unset)")

// self is the caller's own pane. It prevents mistakes, not attacks: any
// process can set these variables. A command Codex runs is placed by its
// thread instead, since a daemon-backed Codex passes the wrong pane.
func self(ctx context.Context, env Env) (envelope.Address, error) {
	session, block := env.Getenv("REX_SESSION"), env.Getenv("REX_BLOCK")
	if env.Getenv("ANTIGRAVITY_CONVERSATION_ID") != "" {
		bindAgy(ctx, env) // agy passes its credentials to every command it runs
	}
	if thread := env.Getenv("CODEX_THREAD_ID"); thread != "" {
		var err error
		if session, block, err = codexPane(ctx, env, thread, session, block); err != nil {
			return envelope.Address{}, err
		}
	}
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

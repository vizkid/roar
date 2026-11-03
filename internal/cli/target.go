package cli

import (
	"context"
	"errors"

	"github.com/vizkid/rexkit/rex"
	"github.com/vizkid/roar/internal/envelope"
)

// resolveTarget is the target rule send and ready share: an optional host
// prefix, then the labels and hints rex.Resolve knows, then "peer" for the
// other pane in the caller's own tab. A label "peer" still wins, like any
// label over a hint. Peer has no host: it is relative to the caller's pane.
func resolveTarget(ctx context.Context, env Env, raw string) (envelope.Address, rex.Client, error) {
	host, target, err := splitHost(ctx, env.Rex, raw)
	if err != nil {
		return envelope.Address{}, env.Rex, err
	}
	c := env.Rex
	c.Server = host
	to, err := rex.Resolve(ctx, c, target)
	if host == "" && errors.Is(err, rex.ErrNoTarget) {
		if uerr := unknownHost(raw); uerr != nil {
			return to, c, uerr
		}
	}
	if host != "" || !errors.Is(err, rex.ErrNoTarget) {
		return to, c, err
	}
	if target != "peer" {
		return to, c, elsewhere(ctx, c, target, err)
	}
	me, serr := self(ctx, env)
	if serr != nil {
		return to, c, serr
	}
	to, err = rex.Peer(ctx, c, me.Session, me.Block)
	return to, c, err
}

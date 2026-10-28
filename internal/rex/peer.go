package rex

import (
	"context"

	"github.com/vizkid/roar/internal/envelope"
)

// Peer is the other pane in block's own window, for two agents side by side.
// Among several others, the only agent pane wins.
func Peer(ctx context.Context, c Client, session, block string) (envelope.Address, error) {
	addr, err := server(ctx, c)
	if err != nil {
		return addr, err
	}
	terms, err := terminals(ctx, c, session)
	if err != nil {
		return addr, err
	}
	window := ""
	for _, b := range terms {
		if b.ID == block {
			window = b.WindowID
		}
	}
	var others []Block
	for _, b := range terms {
		if b.ID != block && window != "" && b.WindowID == window {
			others = append(others, b)
		}
	}
	addr.Session = session
	if sessions, err := c.Sessions(ctx); err == nil {
		for _, s := range sessions {
			if s.ID == session {
				addr.Label = s.Label
			}
		}
	}
	addr.Block, err = choose(ctx, c, session, others, "peer")
	return addr, err
}

// server is the address of c's server, with no session or block yet.
func server(ctx context.Context, c Client) (envelope.Address, error) {
	w, err := c.Whoami(ctx)
	if err != nil {
		return envelope.Address{}, err
	}
	addr := envelope.Address{HostID: w.HostID, Endpoint: w.Endpoint}
	if u := w.Tailscale.HTTPS.URL; u != "" {
		addr.Endpoint = u
	}
	return addr, nil
}

package rex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vizkid/roar/internal/envelope"
)

var (
	ErrNoTarget  = errors.New("no_such_target")
	ErrAmbiguous = errors.New("ambiguous_target")
)

// Resolve turns a target into a pane address on c's server. A target is a
// full block ID, or session-label[/pane], where pane is a pane label or the
// end of a block ID. Without a pane, a session's only pane wins, then its
// only agent pane. Anything else is ambiguous and lists the candidates.
func Resolve(ctx context.Context, c Client, target string) (envelope.Address, error) {
	w, err := c.Whoami(ctx)
	if err != nil {
		return envelope.Address{}, err
	}
	addr := envelope.Address{HostID: w.HostID, Endpoint: w.Endpoint}
	if u := w.Tailscale.HTTPS.URL; u != "" {
		addr.Endpoint = u
	}
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return addr, err
	}
	if strings.HasPrefix(target, "block:") {
		return findBlock(ctx, c, addr, sessions, target)
	}
	label, pane, _ := strings.Cut(target, "/")
	var matches []Session
	for _, s := range sessions {
		if s.Label == label {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return addr, fmt.Errorf("%w: no session labelled %q", ErrNoTarget, label)
	case 1:
	default:
		ids := make([]string, len(matches))
		for i, s := range matches {
			ids[i] = s.ID
		}
		return addr, fmt.Errorf("%w: %d sessions are labelled %q: %s", ErrAmbiguous, len(matches), label, strings.Join(ids, ", "))
	}
	addr.Session, addr.Label = matches[0].ID, label
	block, err := pickBlock(ctx, c, addr.Session, pane)
	addr.Block = block
	return addr, err
}

func findBlock(ctx context.Context, c Client, addr envelope.Address, sessions []Session, id string) (envelope.Address, error) {
	for _, s := range sessions {
		blocks, err := c.Blocks(ctx, s.ID)
		if err != nil {
			continue // the session closed while we looked
		}
		for _, b := range blocks {
			if b.ID == id {
				addr.Session, addr.Block, addr.Label = s.ID, b.ID, s.Label
				return addr, nil
			}
		}
	}
	return addr, fmt.Errorf("%w: no pane %s on this server", ErrNoTarget, id)
}

func pickBlock(ctx context.Context, c Client, session, pane string) (string, error) {
	all, err := c.Blocks(ctx, session)
	if err != nil {
		return "", err
	}
	var terms []Block
	for _, b := range all {
		if b.Terminal() {
			terms = append(terms, b)
		}
	}
	if pane != "" {
		var hits []Block
		for _, b := range terms {
			if b.Label == pane || (len(pane) >= 6 && strings.HasSuffix(b.ID, pane)) {
				hits = append(hits, b)
			}
		}
		return one(hits, fmt.Sprintf("pane %q", pane))
	}
	if len(terms) == 1 {
		return terms[0].ID, nil
	}
	var agents []Block
	for _, b := range terms {
		if p, err := c.Process(ctx, session, b.ID); err == nil && p.Foreground != nil && p.Foreground.Agent() != "" {
			agents = append(agents, b)
		}
	}
	if len(agents) == 1 {
		return agents[0].ID, nil
	}
	return one(terms, "a pane; name one as session/pane")
}

func one(blocks []Block, what string) (string, error) {
	switch len(blocks) {
	case 1:
		return blocks[0].ID, nil
	case 0:
		return "", fmt.Errorf("%w: no match for %s", ErrNoTarget, what)
	}
	list := make([]string, len(blocks))
	for i, b := range blocks {
		list[i] = fmt.Sprintf("%s (%s)", b.ID, b.Label)
	}
	return "", fmt.Errorf("%w: choose %s: %s", ErrAmbiguous, what, strings.Join(list, ", "))
}

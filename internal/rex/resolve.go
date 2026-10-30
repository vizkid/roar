package rex

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vizkid/roar/internal/envelope"
)

var (
	ErrNoTarget  = errors.New("no_such_target")
	ErrAmbiguous = errors.New("ambiguous_target")
)

// Resolve turns a target into a pane address on c's server. A target is a
// full block ID, session-label[/pane], pane@session-label, or a bare window
// or pane label from any session. A pane is a pane label, a window label, or
// the end of a block ID. Among several panes, the only agent pane wins.
// Anything else is ambiguous and lists the candidates.
func Resolve(ctx context.Context, c Client, target string) (envelope.Address, error) {
	addr, err := server(ctx, c)
	if err != nil {
		return addr, err
	}
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return addr, err
	}
	if strings.HasPrefix(target, "block:") {
		return findBlock(ctx, c, addr, sessions, target)
	}
	label, pane, _ := strings.Cut(target, "/")
	if p, l, ok := strings.Cut(target, "@"); ok && pane == "" {
		label, pane = l, p // pane@session reads like the tab people see
	}
	var matches []Session
	for _, s := range sessions {
		if s.Label == label {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		if pane == "" {
			return byName(ctx, c, addr, sessions, label)
		}
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

// byName finds the one session holding a window or pane labelled name.
func byName(ctx context.Context, c Client, addr envelope.Address, sessions []Session, name string) (envelope.Address, error) {
	var found []Session
	var hits []Block
	for _, s := range sessions {
		named, err := named(ctx, c, s.ID, name)
		if err != nil || len(named) == 0 {
			continue // a session that closed while we looked has no match
		}
		found, hits = append(found, s), named
	}
	switch len(found) {
	case 0:
		return addr, fmt.Errorf("%w: no session, window or pane labelled %q", ErrNoTarget, name)
	case 1:
	default:
		list := make([]string, len(found))
		for i, s := range found {
			list[i] = name + "@" + s.Label
		}
		return addr, fmt.Errorf("%w: %q is in %d sessions; name one: %s", ErrAmbiguous, name, len(found), strings.Join(list, ", "))
	}
	addr.Session, addr.Label = found[0].ID, found[0].Label
	block, err := choose(ctx, c, addr.Session, hits, fmt.Sprintf("a pane labelled %q", name))
	addr.Block = block
	return addr, err
}

func pickBlock(ctx context.Context, c Client, session, pane string) (string, error) {
	if pane == "" {
		terms, err := terminals(ctx, c, session)
		if err != nil {
			return "", err
		}
		return choose(ctx, c, session, terms, "a pane; name one as session/pane")
	}
	hits, err := named(ctx, c, session, pane)
	if err != nil {
		return "", err
	}
	return choose(ctx, c, session, hits, fmt.Sprintf("pane %q", pane))
}

// named lists a session's terminal panes that pane names: by pane label or
// block ID suffix first, then every pane in windows with that label, then
// by hint: the agent a pane runs, or its place in its window.
func named(ctx context.Context, c Client, session, pane string) ([]Block, error) {
	terms, err := terminals(ctx, c, session)
	if err != nil {
		return nil, err
	}
	var hits []Block
	for _, b := range terms {
		if b.Label == pane || (len(pane) >= 6 && strings.HasSuffix(b.ID, pane)) {
			hits = append(hits, b)
		}
	}
	if len(hits) > 0 {
		return hits, nil
	}
	windows, err := c.Windows(ctx, session)
	if err != nil {
		return nil, err
	}
	for _, w := range windows {
		if w.Label != pane {
			continue
		}
		for _, b := range terms {
			if b.WindowID == w.ID {
				hits = append(hits, b)
			}
		}
	}
	if len(hits) > 0 {
		return hits, nil
	}
	return hinted(ctx, c, session, terms, windows, pane), nil
}

// hinted lists the panes an agent name or a position word names.
func hinted(ctx context.Context, c Client, session string, terms []Block, windows []Window, hint string) []Block {
	var hits []Block
	if hint == "claude" || hint == "codex" || hint == "devin" {
		for _, b := range terms {
			if p, err := c.Process(ctx, session, b.ID); err == nil && p.Foreground != nil && p.Foreground.Agent() == hint {
				hits = append(hits, b)
			}
		}
		return hits
	}
	for _, w := range windows {
		placed := atPosition(w.Rects(), hint)
		for _, b := range terms {
			if slices.Contains(placed, b.ID) {
				hits = append(hits, b)
			}
		}
	}
	return hits
}

func terminals(ctx context.Context, c Client, session string) ([]Block, error) {
	all, err := c.Blocks(ctx, session)
	if err != nil {
		return nil, err
	}
	var terms []Block
	for _, b := range all {
		if b.Terminal() {
			terms = append(terms, b)
		}
	}
	return terms, nil
}

// choose picks the only pane, else the only agent pane among them.
func choose(ctx context.Context, c Client, session string, blocks []Block, what string) (string, error) {
	if len(blocks) <= 1 {
		return one(blocks, what, nil)
	}
	var agents []Block
	runs := map[string]string{}
	for _, b := range blocks {
		if p, err := c.Process(ctx, session, b.ID); err == nil && p.Foreground != nil && p.Foreground.Agent() != "" {
			agents = append(agents, b)
			runs[b.ID] = p.Foreground.Agent()
		}
	}
	if len(agents) == 1 {
		return agents[0].ID, nil
	}
	return one(blocks, what, runs)
}

// one returns the only block, or lists the candidates with the agent each
// runs: Rex labels every new pane "shell", so labels alone rarely differ.
func one(blocks []Block, what string, runs map[string]string) (string, error) {
	switch len(blocks) {
	case 1:
		return blocks[0].ID, nil
	case 0:
		return "", fmt.Errorf("%w: no match for %s", ErrNoTarget, what)
	}
	list := make([]string, len(blocks))
	for i, b := range blocks {
		desc := b.Label
		if runs[b.ID] != "" {
			desc += ", " + runs[b.ID]
		}
		list[i] = fmt.Sprintf("%s (%s)", b.ID, desc)
	}
	return "", fmt.Errorf("%w: choose %s: %s", ErrAmbiguous, what, strings.Join(list, ", "))
}

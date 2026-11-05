package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/vizkid/rexkit/rex"
	"github.com/vizkid/roar/internal/claude"
	"github.com/vizkid/roar/internal/codex"
	"github.com/vizkid/roar/internal/ready"
	"github.com/vizkid/roar/internal/store"
)

// paneRow is one pane of `roar panes`. Path is how Roar would deliver to
// the pane now: channel (Claude), codex_native, agy_native, typed, or none
// when no agent runs there. Note says why a native path is not in use.
type paneRow struct {
	Session string `json:"session"`
	Host    string `json:"host,omitempty"`
	Tab     string `json:"tab"`
	Window  string `json:"window,omitempty"`
	Flavor  string `json:"flavor,omitempty"`
	Pane    string `json:"pane"`
	Block   string `json:"block"`
	Agent   string `json:"agent,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Path    string `json:"path"`
	Ready   string `json:"ready"`
	Queued  int    `json:"queued"`
	Note    string `json:"note,omitempty"`
}

func cmdPanes(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("panes", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	all := fs.Bool("all", false, "list panes on this host and every stored host")
	porcelain := fs.Bool("porcelain", false, "print one stable line per pane (for other hosts)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || (*all && len(pos) != 0) {
		return usageErr("roar panes [host] [--all]")
	}
	var rows []paneRow
	if *all {
		rows, err = allPanes(ctx, env)
	} else if len(pos) == 1 {
		rows, err = remotePanes(ctx, env, pos[0])
		for i := range rows {
			rows[i].Host = pos[0]
		}
	} else {
		rows, err = localPanes(ctx, env)
		for i := range rows {
			rows[i].Host = localName()
		}
	}
	if err != nil {
		return err
	}
	switch {
	case *asJSON:
		return writeJSON(env, rows)
	case *porcelain:
		for _, r := range rows {
			fmt.Fprintln(env.Stdout, "ROAR-PANE "+strings.Join(r.fields(), " "))
		}
		return nil
	}
	fmt.Fprintf(env.Stdout, "%-18s %-20s %-24s %-20s %-6s %-6s %-13s %-6s %-26s %s\n", "HOST", "SESSION", "TAB", "PANE", "BLOCK", "AGENT", "PATH", "QUEUED", "READY", "NOTE")
	for _, r := range rows {
		fmt.Fprintf(env.Stdout, "%-18s %-20s %-24s %-20s %-6s %-6s %-13s %-6d %-26s %s\n",
			r.Host, r.Session, r.Tab, r.Pane, shortID(r.Block), r.Agent, r.Path, r.Queued, r.Ready, r.Note)
	}
	return nil
}

func shortID(block string) string { return block[max(0, len(block)-6):] }

func localPanes(ctx context.Context, env Env) ([]paneRow, error) {
	st, err := openStore(env)
	if err != nil {
		return nil, err
	}
	rc := localRex(env)
	sessions, err := rc.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	var rows []paneRow
	var sids []string // session IDs: labels need not be unique
	for _, s := range sessions {
		blocks, err := rc.Blocks(ctx, s.ID)
		if err != nil {
			continue // the session closed while we listed
		}
		windows, _ := rc.Windows(ctx, s.ID)
		for _, b := range blocks {
			if b.Terminal() {
				rows = append(rows, paneRow{Session: s.Label, Pane: b.Label, Tab: tabName(b, windows), Window: b.WindowID, Flavor: b.Flavor, Block: b.ID})
				sids = append(sids, s.ID)
			}
		}
	}
	sem := make(chan struct{}, timings.Int("panes.parallel"))
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			describePane(ctx, rc, st, sids[i], &rows[i])
		}()
	}
	wg.Wait()
	return rows, nil
}

// tabName keeps the tab label separate from the block label.
func tabName(b rex.Block, windows []rex.Window) string {
	for _, w := range windows {
		if w.ID == b.WindowID && w.Label != "" {
			return w.Label
		}
	}
	return ""
}

func describePane(ctx context.Context, rc rex.Client, st *store.Store, session string, r *paneRow) {
	if msgs, err := st.Unfinished(r.Block); err == nil {
		for _, m := range msgs {
			if m.Record.State == store.Queued {
				r.Queued++
			}
		}
	}
	res, p := ready.ProbeProcess(ctx, rc, session, r.Block, 0)
	r.Ready = res.State + ":" + res.Reason
	if res.Verdict == ready.Empty {
		r.Ready = res.State
	}
	if p.Foreground == nil || p.LastExit != nil {
		r.Path = "none"
		return
	}
	r.Agent, r.PID = p.Foreground.Agent(), p.Foreground.PID
	r.Path, r.Note = deliveryPath(ctx, st, r.Block, r.Agent, r.PID)
}

// deliveryPath mirrors the order Attempt tries: a live Claude channel, a
// Codex or agy binding to the pane's own agent, then typing.
func deliveryPath(ctx context.Context, st *store.Store, block, agent string, pid int) (path, note string) {
	if st.AdapterLive(block) {
		return "channel", ""
	}
	switch agent {
	case "":
		return "none", ""
	case "claude":
		if ok, why := claude.ChannelProof(pid); !ok {
			note, _, _ = strings.Cut(why, ":")
			return "typed", note
		}
		return "typed", "channel_not_connected"
	case "codex":
		b, ok := st.CodexBinding(block)
		switch {
		case !ok:
			return "typed", "unbound_codex"
		case b.PID != pid:
			return "typed", "codex_binding_stale"
		}
		if ok, why := codex.Reachable(ctx, b); !ok {
			return "typed", why
		}
		return "codex_native", ""
	case "agy":
		b, ok := st.AgyBinding(block)
		switch {
		case !ok:
			return "typed", "unbound_agy"
		case b.PID != pid:
			return "typed", "agy_binding_stale"
		}
		return "agy_native", ""
	}
	return "typed", ""
}

func (r paneRow) fields() []string {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return strings.ReplaceAll(s, " ", "_")
	}
	return []string{dash(r.Session), dash(r.Pane), r.Block, dash(r.Agent), strconv.Itoa(r.PID), r.Path, dash(r.Ready), strconv.Itoa(r.Queued), dash(r.Note), dash(r.Tab), dash(r.Host), dash(r.Window), dash(r.Flavor)}
}

func remotePanes(ctx context.Context, env Env, host string) ([]paneRow, error) {
	p, err := peerFor(ctx, env, host)
	if err != nil {
		return nil, err
	}
	raw, err := p.Panes(ctx)
	saveRTT(env, p)
	if err != nil {
		return nil, err
	}
	var rows []paneRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	// Older daemons combine the labels. Read missing metadata through Rex.
	rc := env.Rex
	rc.Server = p.Endpoint
	bySession := map[string][]int{}
	for i := range rows {
		if rows[i].Window == "" {
			bySession[rows[i].Session] = append(bySession[rows[i].Session], i)
		}
	}
	if len(bySession) == 0 {
		return rows, nil
	}
	sessions, err := rc.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		indices := bySession[session.Label]
		if len(indices) == 0 {
			continue
		}
		blocks, err := rc.Blocks(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		windows, err := rc.Windows(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		for _, i := range indices {
			for _, b := range blocks {
				if b.ID == rows[i].Block {
					rows[i].Pane, rows[i].Tab, rows[i].Window, rows[i].Flavor = b.Label, tabName(b, windows), b.WindowID, b.Flavor
				}
			}
		}
	}
	return rows, nil
}

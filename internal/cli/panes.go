package cli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/vizkid/roar/internal/claude"
	"github.com/vizkid/roar/internal/codex"
	"github.com/vizkid/roar/internal/ready"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/store"
)

// paneRow is one pane of `roar panes`. Path is how Roar would deliver to
// the pane now: channel (Claude), codex_native, agy_native, typed, or none
// when no agent runs there. Note says why a native path is not in use.
type paneRow struct {
	Session string `json:"session"`
	Pane    string `json:"pane"`
	Block   string `json:"block"`
	Agent   string `json:"agent,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Path    string `json:"path"`
	Ready   string `json:"ready"`
	Queued  int    `json:"queued"`
	Note    string `json:"note,omitempty"`
}

// panesParallel bounds the panes probed at once. Each probe runs about six
// rex calls, and a busy host has dozens of panes.
const panesParallel = 8

func cmdPanes(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("panes", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	porcelain := fs.Bool("porcelain", false, "print one stable line per pane (for other hosts)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageErr("roar panes [host]")
	}
	var rows []paneRow
	if len(pos) == 1 {
		rows, err = remotePanes(ctx, env, pos[0])
	} else {
		rows, err = localPanes(ctx, env)
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
	fmt.Fprintf(env.Stdout, "%-24s %-6s %-6s %-13s %-6s %-26s %s\n", "PANE", "BLOCK", "AGENT", "PATH", "QUEUED", "READY", "NOTE")
	for _, r := range rows {
		fmt.Fprintf(env.Stdout, "%-24s %-6s %-6s %-13s %-6d %-26s %s\n",
			r.Pane+"@"+r.Session, shortID(r.Block), r.Agent, r.Path, r.Queued, r.Ready, r.Note)
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
				rows = append(rows, paneRow{Session: s.Label, Pane: paneName(b, windows), Block: b.ID})
				sids = append(sids, s.ID)
			}
		}
	}
	sem := make(chan struct{}, panesParallel)
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

// paneName is the tab's label, the name people see, else the block's.
// Blocks are mostly labelled "shell".
func paneName(b rex.Block, windows []rex.Window) string {
	for _, w := range windows {
		if w.ID == b.WindowID && w.Label != "" {
			return w.Label
		}
	}
	return b.Label
}

func describePane(ctx context.Context, rc rex.Client, st *store.Store, session string, r *paneRow) {
	if msgs, err := st.Inbox(r.Block); err == nil {
		for _, m := range msgs {
			if m.Record.State == store.Queued {
				r.Queued++
			}
		}
	}
	res := ready.Probe(ctx, rc, session, r.Block, 0)
	r.Ready = res.State + ":" + res.Reason
	if res.Verdict == ready.Empty {
		r.Ready = res.State
	}
	p, err := rc.Process(ctx, session, r.Block)
	if err != nil || p.Foreground == nil || p.LastExit != nil {
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
	return []string{dash(r.Session), dash(r.Pane), r.Block, dash(r.Agent), strconv.Itoa(r.PID), r.Path, dash(r.Ready), strconv.Itoa(r.Queued), dash(r.Note)}
}

func remotePanes(ctx context.Context, env Env, host string) ([]paneRow, error) {
	lines, err := remoteHost(env, host).Lines(ctx, "ROAR-PANE", "panes", "--porcelain")
	if err != nil {
		return nil, err
	}
	undash := func(s string) string {
		if s == "-" {
			return ""
		}
		return s
	}
	var rows []paneRow
	for _, f := range lines {
		if len(f) < 9 {
			continue
		}
		pid, _ := strconv.Atoi(f[4])
		queued, _ := strconv.Atoi(f[7])
		rows = append(rows, paneRow{Session: undash(f[0]), Pane: undash(f[1]), Block: f[2], Agent: undash(f[3]),
			PID: pid, Path: f[5], Ready: undash(f[6]), Queued: queued, Note: undash(f[8])})
	}
	return rows, nil
}

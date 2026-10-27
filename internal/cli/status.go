package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/store"
)

type statusRow struct {
	ID       string `json:"id"`
	To       string `json:"to"`
	State    string `json:"state"`
	Detail   string `json:"detail,omitempty"`
	Proof    string `json:"proof,omitempty"`
	Boundary string `json:"boundary,omitempty"`
}

func cmdStatus(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	porcelain := fs.Bool("porcelain", false, "print one stable line per message (for scripts and other hosts)")
	ids, err := parse(fs, args)
	if err != nil {
		return err
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		me, err := self(ctx, env)
		if err != nil {
			return err
		}
		sent, err := st.Sent(me.Block)
		if err != nil {
			return err
		}
		for _, e := range sent {
			ids = append(ids, e.ID)
		}
	}
	rows := make([]statusRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, statusOf(st, id))
	}
	fillRemote(ctx, env, st, rows)
	if *asJSON {
		return writeJSON(env, rows)
	}
	if *porcelain {
		dash := func(s string) string {
			if s == "" {
				return "-"
			}
			return strings.ReplaceAll(s, " ", "_")
		}
		for _, r := range rows {
			fmt.Fprintf(env.Stdout, "ROAR-STATUS %s %s %s %s %s\n", r.ID, r.State, dash(r.Detail), dash(r.Proof), dash(r.Boundary))
		}
		return nil
	}
	for _, r := range rows {
		line := fmt.Sprintf("%s  %-10s %s", r.ID, r.State, r.To)
		for _, extra := range []string{r.Detail, r.Proof, r.Boundary} {
			if extra != "" {
				line += "  " + extra
			}
		}
		fmt.Fprintln(env.Stdout, line)
	}
	return nil
}

// fillRemote asks other hosts about messages that went there. A host that
// cannot be reached leaves its rows as remote_unreachable.
func fillRemote(ctx context.Context, env Env, st *store.Store, rows []statusRow) {
	byServer := map[string][]int{}
	for i, r := range rows {
		if r.State != "not_found" {
			continue
		}
		e, err := st.SentRecord(r.ID)
		if err != nil || e.To.Endpoint == "" {
			continue
		}
		rows[i].To = envelope.Sanitize(e.To.Label)
		byServer[e.To.Endpoint] = append(byServer[e.To.Endpoint], i)
	}
	for server, idx := range byServer {
		ids := make([]string, len(idx))
		for k, i := range idx {
			ids[k] = rows[i].ID
		}
		got, err := remoteHost(env, server).Status(ctx, ids...)
		found := map[string]bool{}
		for _, g := range got {
			for _, i := range idx {
				if rows[i].ID == g.ID {
					rows[i].State, rows[i].Detail, rows[i].Proof, rows[i].Boundary = g.State, g.Detail, g.Proof, g.Boundary
					found[g.ID] = true
				}
			}
		}
		for _, i := range idx {
			if !found[rows[i].ID] && err != nil {
				rows[i].State, rows[i].Detail = "remote_unreachable", err.Error()
			}
		}
	}
}

func statusOf(st *store.Store, id string) statusRow {
	m, err := st.Get(id)
	if err != nil {
		return statusRow{ID: id, State: "not_found"}
	}
	r := m.Record
	detail := r.HoldReason
	if detail == "" {
		detail = r.Reason
	}
	to := envelope.Sanitize(m.Envelope.To.Label)
	if to == "" {
		to = m.Envelope.To.Block
	}
	return statusRow{ID: id, To: to, State: string(r.State), Detail: detail, Proof: r.Proof, Boundary: r.Boundary}
}

func cmdWithdraw(ctx context.Context, env Env, args []string) error {
	pos, err := parse(flag.NewFlagSet("withdraw", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("roar withdraw <id>")
	}
	me, err := self(ctx, env)
	if err != nil {
		return err
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	if err := st.Withdraw(pos[0], me.Block, env.Now()); err != nil {
		return fmt.Errorf("refused: %v", err)
	}
	return nil
}

func cmdResolve(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	read := fs.Bool("read", false, "the recipient did get it")
	requeue := fs.Bool("requeue", false, "the recipient did not get it; deliver again")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *read == *requeue {
		return usageErr("roar resolve <id> --read|--requeue")
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	c, err := st.Claim(pos[0])
	if err != nil {
		return fmt.Errorf("refused: %v", err)
	}
	defer c.Release()
	if s := c.Message().Record.State; s != store.Unknown {
		return fmt.Errorf("refused: %s is %s; only unknown messages need resolving", pos[0], s)
	}
	to, note := store.Queued, "operator_requeued"
	if *read {
		to, note = store.Read, "operator"
	}
	return c.Move(to, env.Now(), func(r *store.Record) {
		r.Reason = strings.TrimSpace(note)
		if to == store.Read {
			r.Proof = note
		}
	})
}

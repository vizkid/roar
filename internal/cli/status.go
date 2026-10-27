package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/vizkid/ping/internal/envelope"
	"github.com/vizkid/ping/internal/store"
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
	if *asJSON {
		return writeJSON(env, rows)
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
		return usageErr("ping withdraw <id>")
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
		return usageErr("ping resolve <id> --read|--requeue")
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

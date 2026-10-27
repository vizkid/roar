package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/vizkid/ping/internal/envelope"
	"github.com/vizkid/ping/internal/rex"
	"github.com/vizkid/ping/internal/store"
)

func cmdSend(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	request := fs.Bool("request", false, "ask the recipient to act")
	reply := fs.String("reply", "", "answer this message from your inbox")
	key := fs.String("key", "", "idempotency key; a retried send with the same key is stored once")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	from, err := self(ctx, env)
	if err != nil {
		return err
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	to, words, err := recipient(ctx, env, st, from, pos, *reply)
	if err != nil {
		return err
	}
	text := joinText(words)
	if len(words) == 0 {
		b, err := io.ReadAll(io.LimitReader(env.Stdin, envelope.MaxText+1))
		if err != nil {
			return err
		}
		text = string(b)
	}
	now := env.Now()
	e := envelope.Envelope{
		ID: envelope.NewID(now), From: from, To: to, Kind: envelope.Inform,
		Text: text, ReplyTo: *reply, IdempotencyKey: *key, CreatedAt: now,
	}
	if *request {
		e.Kind = envelope.Request
	}
	if *key != "" {
		e.ID = envelope.KeyedID(from.HostID, from.Block, *key)
	}
	if err := e.Validate(); err != nil {
		return err
	}
	st.Prune(now)

	if to.HostID != from.HostID {
		r, err := sendRemote(ctx, env, e)
		if err != nil {
			return err
		}
		if err := st.RecordSent(e); err != nil {
			return err
		}
		return printResult(env, e.ID, r.State, r.Reason, *asJSON)
	}
	if _, err := st.Put(e, now); err != nil {
		return err
	}
	if err := st.RecordSent(e); err != nil {
		return err
	}
	// One delivery pass. A native adapter, if one serves the pane, has
	// already seen the new message; the fallback stands aside for it.
	if _, err := deliverer(env, st).Block(ctx, to.Block); err != nil {
		fmt.Fprintf(env.Stderr, "ping: delivery pass: %v\n", err)
	}
	m, err := st.Get(e.ID)
	if err != nil {
		return err
	}
	return printResult(env, e.ID, string(m.Record.State), m.Record.HoldReason, *asJSON)
}

// recipient finds the address for a send, and the words left for the body.
func recipient(ctx context.Context, env Env, st *store.Store, from envelope.Address, pos []string, reply string) (envelope.Address, []string, error) {
	if reply != "" {
		orig, err := st.Get(reply)
		if err != nil || orig.Envelope.To.Block != from.Block {
			return envelope.Address{}, nil, fmt.Errorf("no_such_message: %s is not in this pane's inbox", reply)
		}
		return orig.Envelope.From, pos, nil
	}
	if len(pos) == 0 {
		return envelope.Address{}, nil, usageErr("ping send <target> [text]")
	}
	host, target, err := splitHost(ctx, env.Rex, pos[0])
	if err != nil {
		return envelope.Address{}, nil, err
	}
	c := env.Rex
	c.Server = host
	to, err := rex.Resolve(ctx, c, target)
	return to, pos[1:], err
}

func printResult(env Env, id, state, reason string, asJSON bool) error {
	if !asJSON {
		fmt.Fprintln(env.Stdout, id)
		return nil
	}
	return json.NewEncoder(env.Stdout).Encode(map[string]any{"id": id, "state": state, "hold_reason": reason})
}

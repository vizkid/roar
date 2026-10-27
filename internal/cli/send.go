package cli

import (
	"context"
	"encoding/json"
	"errors"
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

	var to envelope.Address
	words := pos
	if *reply != "" {
		orig, err := st.Get(*reply)
		if err != nil || orig.Envelope.To.Block != from.Block {
			return fmt.Errorf("no_such_message: %s is not in this pane's inbox", *reply)
		}
		to = orig.Envelope.From
	} else {
		if len(pos) == 0 {
			return usageErr("ping send <target> [text]")
		}
		if to, err = rex.Resolve(ctx, env.Rex, pos[0]); err != nil {
			return err
		}
		words = pos[1:]
	}
	if to.HostID != from.HostID {
		return errors.New("remote_unsupported: sending to another host is not built yet")
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
	st.Prune(now)
	if _, err := st.Put(e, now); err != nil {
		return err
	}
	if err := st.RecordSent(e); err != nil {
		return err
	}
	m, err := st.Get(e.ID)
	if err != nil {
		return err
	}
	return printSent(env, m, *asJSON)
}

func printSent(env Env, m store.Message, asJSON bool) error {
	if !asJSON {
		fmt.Fprintln(env.Stdout, m.Envelope.ID)
		return nil
	}
	return json.NewEncoder(env.Stdout).Encode(map[string]any{
		"id":          m.Envelope.ID,
		"state":       m.Record.State,
		"hold_reason": m.Record.HoldReason,
	})
}

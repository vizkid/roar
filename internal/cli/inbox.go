package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/store"
)

func cmdInbox(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	all := fs.Bool("all", false, "include messages already read or expired")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	me, err := self(ctx, env)
	if err != nil {
		return err
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	msgs, err := st.Inbox(me.Block)
	if err != nil {
		return err
	}
	var shown []store.Message
	for _, m := range msgs {
		if *all || (m.Record.State != store.Read && m.Record.State != store.Expired) {
			shown = append(shown, m)
		}
	}
	if *asJSON {
		return writeJSON(env, shown)
	}
	for _, m := range shown {
		first, _, _ := strings.Cut(m.Envelope.Text, "\n")
		fmt.Fprintf(env.Stdout, "%s  %-10s %-8s %s  %s\n", m.Envelope.ID, m.Record.State,
			envelope.NormalizeKind(m.Envelope.Kind), envelope.Sanitize(m.Envelope.From.Label), envelope.Sanitize(first))
	}
	return nil
}

func cmdRead(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	mark := fs.Bool("mark", false, "mark the message read")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("roar read <id> [--mark]")
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	m, err := st.Get(pos[0])
	if err != nil {
		return fmt.Errorf("no_such_message: %s", pos[0])
	}
	if *mark {
		me, err := self(ctx, env)
		if err != nil {
			return err
		}
		if m.Envelope.To.Block != me.Block {
			return fmt.Errorf("not_recipient: only the recipient pane may mark %s read", pos[0])
		}
		if err := markRead(env, st, m); err != nil {
			return err
		}
	}
	fmt.Fprintln(env.Stdout, m.Render())
	return nil
}

func markRead(env Env, st *store.Store, m store.Message) error {
	if m.Record.State == store.Read {
		return nil
	}
	c, err := st.Claim(m.Envelope.ID)
	if err != nil {
		return fmt.Errorf("busy: %s is being delivered; try again", m.Envelope.ID)
	}
	defer c.Release()
	if c.Message().Record.State.Final() {
		return nil
	}
	return c.Move(store.Read, env.Now(), func(r *store.Record) { r.Proof = "marked" })
}

func writeJSON(env Env, v any) error {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

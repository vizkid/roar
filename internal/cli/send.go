package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/store"
)

func cmdSend(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	request := fs.Bool("request", false, "ask the recipient to act")
	reply := fs.String("reply", "", "answer this message from your inbox")
	key := fs.String("key", "", "idempotency key; a retried send with the same key is stored once")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	var paths fileList
	fs.Var(&paths, "file", "attach a file (repeatable)")
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
	files, err := attach(&e, paths)
	if err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	st.Prune(now)

	if to.HostID != from.HostID {
		r, err := sendRemote(ctx, env, e, files)
		if err != nil {
			return err
		}
		if err := st.RecordSent(e); err != nil {
			return err
		}
		return printResult(env, e.ID, r.State, r.Reason, *asJSON)
	}
	if _, err := st.PutWithFiles(e, now, files); err != nil {
		return err
	}
	if err := st.RecordSent(e); err != nil {
		return err
	}
	// One delivery pass. A native adapter, if one serves the pane, has
	// already seen the new message; the fallback stands aside for it.
	if _, err := deliverer(env, st).Block(ctx, to.Block); err != nil {
		fmt.Fprintf(env.Stderr, "roar: delivery pass: %v\n", err)
	}
	m, err := settle(ctx, st, e.ID)
	if err != nil {
		return err
	}
	if err := startWatcher(ctx, env, st); err != nil {
		fmt.Fprintf(env.Stderr, "roar: starting watcher: %v\n", err)
	}
	return printResult(env, e.ID, string(m.Record.State), m.Record.HoldReason, *asJSON)
}

// settleFor bounds how long send waits for a native adapter's outcome.
// Codex and Claude settle in about a second; waiting here saves the sending
// agent a whole roar status turn.
const settleFor = 3 * time.Second

// settle waits while the message is in flight: claimed by an adapter, or
// queued for one (no hold reason yet, or the fallback's native_adapter).
func settle(ctx context.Context, st *store.Store, id string) (store.Message, error) {
	deadline := time.Now().Add(settleFor)
	for {
		m, err := st.Get(id)
		if err != nil {
			return m, err
		}
		r := m.Record
		moving := r.State == store.Written || r.State == store.Accepted || r.State == store.Submitting ||
			(r.State == store.Queued && (r.HoldReason == "" || r.HoldReason == "native_adapter"))
		if !moving || time.Now().After(deadline) {
			return m, nil
		}
		select {
		case <-ctx.Done():
			return m, nil
		case <-time.After(100 * time.Millisecond):
		}
	}
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
		return envelope.Address{}, nil, usageErr("roar send <target> [text]")
	}
	to, _, err := resolveTarget(ctx, env, pos[0])
	return to, pos[1:], err
}

// printResult prints "<id> <state> [hold reason]". Scripts take the first
// field, or use --json.
func printResult(env Env, id, state, reason string, asJSON bool) error {
	if !asJSON {
		fmt.Fprintln(env.Stdout, strings.TrimSpace(id+" "+state+" "+reason))
		return nil
	}
	return json.NewEncoder(env.Stdout).Encode(map[string]any{"id": id, "state": state, "hold_reason": reason})
}

type fileList []string

func (f *fileList) String() string     { return strings.Join(*f, ",") }
func (f *fileList) Set(v string) error { *f = append(*f, v); return nil }

// attach reads the files and lists them on the envelope.
func attach(e *envelope.Envelope, paths []string) (map[string][]byte, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var total int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("not_a_file: %s", p)
		}
		if total += info.Size(); total > envelope.MaxFiles {
			return nil, fmt.Errorf("%w: attachments exceed %d bytes", store.ErrLimit, envelope.MaxFiles)
		}
	}
	files := map[string][]byte{}
	for i, name := range envelope.UniqueNames(paths) {
		data, err := os.ReadFile(paths[i])
		if err != nil {
			return nil, err
		}
		files[name] = data
		e.Files = append(e.Files, envelope.FileFor(name, data))
	}
	return files, nil
}

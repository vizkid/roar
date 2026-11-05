package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/remote"
	"github.com/vizkid/roar/internal/store"
)

func cmdSend(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "resolve the recipient without storing or sending a message")
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
	if *dryRun && *reply == "" {
		if len(pos) == 0 {
			return usageErr("roar send --dry-run <target>")
		}
		to, rc, err := resolveTarget(ctx, env, pos[0])
		if err != nil {
			return err
		}
		return printTarget(ctx, env, rc, to, *asJSON)
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
	if *dryRun {
		rc := env.Rex
		if to.HostID != from.HostID {
			rc.Server = to.Endpoint
		}
		return printTarget(ctx, env, rc, to, *asJSON)
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
		if errors.Is(err, remote.ErrUnknown) {
			// The message may have landed. Report its ID, as for any
			// unknown outcome, so the sender can check before resending.
			fmt.Fprintf(env.Stderr, "roar: %v\n", err)
			r, err = remote.Result{ID: e.ID, State: "unknown", Reason: "remote_outcome_unknown"}, nil
		}
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

// settle waits while the message is in flight: claimed by an adapter, or
// queued for one (no hold reason yet, or the fallback's native_adapter).
func settle(ctx context.Context, st *store.Store, id string) (store.Message, error) {
	// sender.settle bounds the wait for a native adapter's outcome. Codex
	// and Claude settle in about a second; waiting here saves the sending
	// agent a whole roar status turn.
	deadline := time.Now().Add(timings.Duration("sender.settle"))
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
		case <-time.After(timings.Duration("sender.settle_poll")):
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

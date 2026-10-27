package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/vizkid/roar/internal/fallback"
	"github.com/vizkid/roar/internal/ready"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/store"
)

func deliverer(env Env, st *store.Store) *fallback.Deliverer {
	return &fallback.Deliverer{Store: st, Rex: env.Rex, Now: env.Now}
}

func cmdReady(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("roar ready <target>")
	}
	to, err := rex.Resolve(ctx, env.Rex, pos[0])
	if err != nil {
		return err
	}
	r := ready.Probe(ctx, env.Rex, to.Session, to.Block, 0)
	if *asJSON {
		if err := writeJSON(env, r); err != nil {
			return err
		}
	} else if r.Verdict == ready.Empty {
		fmt.Fprintln(env.Stdout, "empty")
	} else {
		fmt.Fprintf(env.Stdout, "%s: %s\n", r.State, r.Reason)
	}
	switch r.Verdict {
	case ready.Wait:
		return silentExit(exitWait)
	case ready.Refuse:
		return silentExit(exitRefused)
	}
	return nil
}

func cmdDeliver(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("deliver", flag.ContinueOnError)
	watch := fs.Bool("watch", false, "keep running; deliver held messages as panes come to rest")
	asJSON := fs.Bool("json", false, "print as JSON")
	targets, err := parse(fs, args)
	if err != nil {
		return err
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	d := deliverer(env, st)
	if *watch {
		if len(targets) > 0 {
			return usageErr("roar deliver --watch takes no targets")
		}
		return d.Watch(watchContext(ctx))
	}
	var outs []fallback.Outcome
	if len(targets) == 0 {
		if outs, err = d.All(ctx); err != nil {
			return err
		}
	}
	for _, t := range targets {
		to, err := rex.Resolve(ctx, env.Rex, t)
		if err != nil {
			return err
		}
		out, err := d.Block(ctx, to.Block)
		if err != nil {
			return err
		}
		if out != nil {
			outs = append(outs, *out)
		}
	}
	if *asJSON {
		return writeJSON(env, outs)
	}
	for _, o := range outs {
		fmt.Fprintf(env.Stdout, "%s  %-10s %s\n", o.ID, o.State, o.Reason)
	}
	return nil
}

// silentExit carries an exit code whose explanation is already printed.
type silentExit int

func (s silentExit) Error() string { return "" }

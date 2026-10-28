package cli

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/vizkid/roar/internal/fallback"
	"github.com/vizkid/roar/internal/ready"
	"github.com/vizkid/roar/internal/remote"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/store"
)

func deliverer(env Env, st *store.Store) *fallback.Deliverer {
	return &fallback.Deliverer{Store: st, Rex: localRex(env), Now: env.Now}
}

// localRex finds rex through REX_BIN_DIR when Rex set it: an MCP child, a
// hook, or a detached block may lack a login PATH.
func localRex(env Env) rex.Client {
	c := env.Rex
	if dir := env.Getenv("REX_BIN_DIR"); dir != "" {
		c.Bin = filepath.Join(dir, "rex")
	}
	return c
}

// startWatcher starts a watcher in a detached block when a held message
// needs one and none runs. Callers write their holds first, so a watcher
// that is stopping at the same moment still sees them.
func startWatcher(ctx context.Context, env Env, st *store.Store) error {
	if env.Self == "" || !st.WatcherFree() {
		return nil
	}
	if need, err := deliverer(env, st).NeedsWatcher(); err != nil || !need {
		return err
	}
	c := localRex(env)
	cmd := []string{"/usr/bin/env", "ROAR_HOME=" + st.Root()}
	if bin, err := exec.LookPath(cmp.Or(c.Bin, "rex")); err == nil {
		cmd = append(cmd, "REX_BIN_DIR="+filepath.Dir(bin))
	}
	cmd = append(cmd, env.Self, "deliver", "--watch", "--until-idle")
	return remote.Host{Rex: c, Session: env.Relay}.Detach(ctx, "roar-watch", cmd)
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
	to, c, err := resolveTarget(ctx, env, pos[0])
	if err != nil {
		return err
	}
	r := ready.Probe(ctx, c, to.Session, to.Block, 0)
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
	untilIdle := fs.Bool("until-idle", false, "with --watch: exit once no held message needs a watcher")
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
	if *untilIdle && !*watch {
		return usageErr("roar deliver --until-idle needs --watch")
	}
	if *watch {
		if len(targets) > 0 {
			return usageErr("roar deliver --watch takes no targets")
		}
		return d.Watch(watchContext(ctx), *untilIdle)
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
	if err := startWatcher(ctx, env, st); err != nil {
		fmt.Fprintf(env.Stderr, "roar: starting watcher: %v\n", err)
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

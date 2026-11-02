package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/fallback"
	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/roard"
)

// cmdDaemon runs roard, the one Roar service per host. The service manager
// starts it; see docs/ROARD.md.
func cmdDaemon(ctx context.Context, env Env, args []string) error {
	if len(args) != 0 {
		return usageErr("roar daemon")
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	var cur atomic.Pointer[config.Settings]
	s, err := loadSettings(env, "")
	if err != nil {
		return err
	}
	cur.Store(s)
	log := &rotating{path: filepath.Join(st.Root(), "roard.log"), max: func() int64 { return cur.Load().Size("roard.log.max_size") }}
	logf := func(format string, a ...any) {
		fmt.Fprintf(log, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}
	go watchConfig(ctx, env, &cur, logf)
	w, err := env.Rex.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("rex whoami: %w", err)
	}
	d := deliverer(env, st)
	logf("roard %s starting, store %s", version(), st.Root())
	return roard.Run(ctx, roard.Deps{
		Store: st, Settings: cur.Load, Endpoint: rexctl.LocalEndpoint(env.Getenv, env.Getenv("HOME")),
		HostID: w.HostID, Version: version(), Label: env.Getenv("ROAR_ROARD_LABEL"),
		Delivery: blockDelivery{d, logf},
		Panes:    func(ctx context.Context) (any, error) { return localPanes(ctx, env) },
		Foreground: func(ctx context.Context, session, block string) (string, error) {
			p, err := localRex(env).Process(ctx, session, block)
			if err != nil || p.Foreground == nil {
				return "", err
			}
			return p.Foreground.Name, nil
		},
		RelayLabel: env.Getenv("ROAR_RELAY_LABEL"), // tests keep roard away from the real roar-relay
		UID:        os.Getuid(), Logf: logf,
	})
}

// blockDelivery is the fallback's one attempt per pane, as roard calls it.
type blockDelivery struct {
	d    *fallback.Deliverer
	logf func(string, ...any)
}

func (b blockDelivery) Block(ctx context.Context, block string) {
	if out, err := b.d.Block(ctx, block); err != nil {
		b.logf("deliver %s: %v", block, err)
	} else if out != nil && out.Reason != "claimed_elsewhere" {
		// A send delivers itself while roard wakes on its notify mark;
		// skipping that claim is the normal case, not news.
		b.logf("deliver %s: %s %s %s", block, out.ID, out.State, out.Reason)
	}
}

// watchConfig reloads the settings when the config file changes. A bad file
// is logged and the last good settings stay.
func watchConfig(ctx context.Context, env Env, cur *atomic.Pointer[config.Settings], logf func(string, ...any)) {
	path := config.DefaultPath(env.Getenv, env.Getenv("HOME"))
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		logf("config watch: %v", err)
		return
	}
	defer fsw.Close()
	if err := fsw.Add(dir); err != nil {
		logf("config watch: %v", err)
		return
	}
	for {
		select {
		case ev := <-fsw.Events:
			if filepath.Base(ev.Name) != filepath.Base(path) {
				continue
			}
			s, warn, err := config.Load(config.FromEnv(env.Getenv, env.Getenv("HOME"), ""))
			for _, w := range warn {
				logf("config: %s", w)
			}
			if err != nil {
				logf("config: %v; keeping the last good settings", err)
				continue
			}
			for _, k := range config.Keys() {
				if k.Restart && s.Value(k.Name) != cur.Load().Value(k.Name) {
					logf("config: %s changes after a restart", k.Name)
				}
			}
			cur.Store(s)
			logf("config reloaded")
		case <-fsw.Errors:
		case <-ctx.Done():
			return
		}
	}
}

// rotating is roard's log: at max bytes it moves to <path>.1 and starts over.
type rotating struct {
	path string
	max  func() int64
	mu   sync.Mutex
	f    *os.File
}

func (r *rotating) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		r.f = f
	}
	if st, err := r.f.Stat(); err == nil && st.Size()+int64(len(b)) > r.max() {
		r.f.Close()
		os.Rename(r.path, r.path+".1")
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			r.f = nil
			return 0, err
		}
		r.f = f
	}
	return r.f.Write(b)
}

var _ io.Writer = (*rotating)(nil)

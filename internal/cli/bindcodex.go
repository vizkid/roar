package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/vizkid/roar/internal/store"
)

const bindCodexUsage = "roar bind codex --thread <id> --pid <pid> [--codex-home <dir>]"

// threadID is what a Codex thread ID may hold. It is only checked so a
// bad argument cannot reach a file name or a daemon call.
var threadID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// cmdBindCodex records which pane a daemon-backed Codex thread runs in. A
// launcher runs it: Burns's launch watcher sees the thread the new TUI
// starts on the daemon, and passes the TUI's PID. The watcher is not a
// child of Codex, so ancestry cannot prove the pane, as it does for the
// SessionStart hook. The proof is that Rex reports that PID as the pane's
// foreground Codex. Like REX_BLOCK itself, this prevents mistakes, not
// attacks.
func cmdBindCodex(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("bind codex", flag.ContinueOnError)
	thread := fs.String("thread", "", "the Codex thread ID")
	pid := fs.Int("pid", 0, "the pane's Codex process ID")
	home := fs.String("codex-home", "", "the Codex home the thread lives in (default $CODEX_HOME or ~/.codex)")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 0 || *thread == "" || *pid <= 0 {
		return usageErr(bindCodexUsage)
	}
	if !threadID.MatchString(*thread) {
		return errors.New("invalid_thread: a thread ID holds only letters, digits, '.', '_' and '-'")
	}
	if *home == "" {
		*home = codexHome(env)
	}
	if abs, err := filepath.Abs(*home); err == nil {
		*home = abs
	}
	session, block := env.Getenv("REX_SESSION"), env.Getenv("REX_BLOCK")
	if session == "" || block == "" {
		return errNotInPane
	}
	rc := localRex(env)
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	p, err := rc.Process(ctx, session, block)
	if err != nil {
		return err
	}
	switch {
	case p.LastExit != nil || p.Foreground == nil || p.Foreground.Agent() != "codex":
		return errors.New("not_codex_pane: the pane's foreground program is not codex")
	case p.Foreground.PID != *pid:
		return fmt.Errorf("pid_not_foreground: the pane's Codex is process %d, not %d", p.Foreground.PID, *pid)
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	// A thread runs in one pane. A binding elsewhere whose Codex still runs
	// wins; a stale one goes, so the thread lookup cannot find it first.
	for {
		other, b, ok := st.CodexThread(*thread)
		if !ok || other == block {
			break
		}
		if rc.Runs(ctx, b.Session, other, b.PID, "codex") {
			return fmt.Errorf("thread_bound_elsewhere: thread %s is bound to live pane %s", *thread, other)
		}
		st.UnbindCodex(other)
	}
	b := store.CodexBinding{Thread: *thread, Session: session, CodexHome: *home, PID: *pid, At: env.Now()}
	if err := st.BindCodex(block, b); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "bound %s\n", block)
	return nil
}

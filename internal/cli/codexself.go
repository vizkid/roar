package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vizkid/roar/internal/codex"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/store"
)

var errUnbound = errors.New("unbound_codex_thread: Roar cannot tell which pane this Codex thread is in yet; " +
	"it learns that from the first Roar message the thread receives")

// deliveredIDs is a variable so tests can stand in for the daemon.
var deliveredIDs = codex.DeliveredIDs

// codexPane finds the pane of the Codex thread running this command. A
// daemon-backed Codex runs commands in its shared daemon, so REX_SESSION
// and REX_BLOCK name whatever pane started the daemon, not this thread's.
// In order: a live binding for the thread; proof that the last message
// written into a Codex pane started a turn in this thread; the inherited
// pane, only when its foreground process is an ancestor of this one.
func codexPane(ctx context.Context, env Env, thread, session, block string) (string, string, error) {
	st, err := openStore(env)
	if err != nil {
		return "", "", err
	}
	if bound, b, ok := st.CodexThread(thread); ok && env.Rex.Runs(ctx, b.Session, bound, b.PID, "codex") {
		return b.Session, bound, nil
	}
	if s, b, ok := provePane(ctx, env, st, thread); ok {
		return s, b, nil
	}
	if session != "" && block != "" && ownPane(ctx, env.Rex, session, block) {
		return session, block, nil
	}
	return "", "", errUnbound
}

// provePane binds thread to the pane whose newest written message heads a
// turn in it. Only the newest counts: after /new the pane shows another
// thread, and an older message would bind the thread it left.
func provePane(ctx context.Context, env Env, st *store.Store, thread string) (string, string, bool) {
	home := codexHome(env)
	ids, err := deliveredIDs(ctx, home, thread)
	if err != nil {
		return "", "", false
	}
	for _, id := range ids {
		m, err := st.Get(id)
		if err != nil || newestWritten(st, m.Envelope.To.Block) != id {
			continue
		}
		to := m.Envelope.To
		p, err := env.Rex.Process(ctx, to.Session, to.Block)
		if err != nil || p.LastExit != nil || p.Foreground == nil || p.Foreground.Agent() != "codex" {
			continue
		}
		st.BindCodex(to.Block, store.CodexBinding{Thread: thread, Session: to.Session, CodexHome: home,
			PID: p.Foreground.PID, At: env.Now()})
		return to.Session, to.Block, true
	}
	return "", "", false
}

// newestWritten is the ID of the message last written into block's pane.
func newestWritten(st *store.Store, block string) string {
	msgs, _ := st.Inbox(block)
	var newest string
	var last time.Time
	for _, m := range msgs {
		if at := writtenAt(m.Record); at.After(last) {
			newest, last = m.Envelope.ID, at
		}
	}
	return newest
}

// writtenAt is when the message reached the pane. Typed messages from
// before WrittenAt was recorded fall back to their last state change.
func writtenAt(r store.Record) time.Time {
	if !r.WrittenAt.IsZero() {
		return r.WrittenAt
	}
	switch r.State {
	case store.Submitted, store.Read, store.Unknown:
		return r.UpdatedAt
	}
	return time.Time{}
}

// ownPane reports whether the pane's foreground process is this process
// or one of its ancestors: the pane really runs this command.
func ownPane(ctx context.Context, c rex.Client, session, block string) bool {
	p, err := c.Process(ctx, session, block)
	if err != nil || p.LastExit != nil || p.Foreground == nil {
		return false
	}
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return false
	}
	parent := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			pid, _ := strconv.Atoi(f[0])
			ppid, _ := strconv.Atoi(f[1])
			parent[pid] = ppid
		}
	}
	for pid, n := os.Getpid(), 0; pid > 1 && n < 64; pid, n = parent[pid], n+1 {
		if pid == p.Foreground.PID {
			return true
		}
	}
	return false
}

// codexHome is the Codex home of the Codex running this command.
func codexHome(env Env) string {
	if h := env.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return filepath.Join(env.Getenv("HOME"), ".codex")
}

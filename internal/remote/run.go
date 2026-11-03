// Package remote runs Roar on another Rex host through Rex itself: a
// command in a detached block, input streamed through its terminal, and the
// exit code read back. Roar opens no network listener of its own.
package remote

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vizkid/rexkit/rex"
)

var (
	ErrNoPing  = errors.New("no_roar_on_host")
	ErrRefused = errors.New("refused")
	ErrUnknown = errors.New("remote_outcome_unknown")
)

// DefaultBin is where `roar install` puts Roar on a host. Remote commands
// use an absolute path: a detached block's PATH lacks ~/.local/bin.
const DefaultBin = "$HOME/.local/bin/roar"

const (
	readyMarker = "ROAR-READY"
	endMarker   = "#END#"
	// Chunk is how much input one rex write carries, under Rex's 64 KiB
	// request limit.
	Chunk = 48 << 10
)

// InstallSession labels the windowless session install creates on a host
// that has no session at all. It is destroyed when the call ends.
const InstallSession = "roar-install"

// Host is one Rex server that runs Roar.
type Host struct {
	Rex     rex.Client // Server is the host label or endpoint
	Bin     string     // remote roar path; DefaultBin when empty
	Env     []string   // extra environment for the remote command
	Session string     // label of the session for detached blocks; empty picks an existing one
	Timeout time.Duration
}

// roarCommand runs the remote roar with args.
func (h Host) roarCommand(args ...string) []string {
	bin := h.Bin
	if bin == "" {
		bin = DefaultBin
	}
	// Only $HOME expands; the rest of the path is literal, never shell source.
	if rest, ok := strings.CutPrefix(bin, "$HOME/"); ok {
		bin = `"$HOME"/` + shellQuote(rest)
	} else {
		bin = shellQuote(bin)
	}
	cmd := []string{"/usr/bin/env"}
	cmd = append(cmd, h.Env...)
	return append(append(cmd, "/bin/sh", "-c", "exec "+bin+` "$@"`, "roar"), args...)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// run starts command in a detached block. With raw input it waits for the
// command's ready marker, streams base64 input and the end marker, then
// waits for the exit code. The block is closed before returning.
func (h Host) run(ctx context.Context, command []string, input []byte, wait bool) (code int, screen string, err error) {
	session, done, err := h.session(ctx)
	if err != nil {
		return 0, "", err
	}
	defer done()
	req := map[string]any{"session_id": session, "flavor": "com.superlogical.terminal.shell", "label": "roar", "options": map[string]any{
		// login false: under /usr/bin/login the exit code is always 0.
		"shell": "none", "login": false, "exit": map[string]any{"on_completion": false},
		"size": map[string]any{"columns": 4000, "rows": 50}, "command": command,
	}}
	var created struct {
		BlockID string `json:"block_id"`
	}
	if err := h.Rex.JSON(ctx, &created, "api", "call", "session.new_block", mustJSON(req)); err != nil {
		return 0, "", err
	}
	block := created.BlockID
	defer h.Rex.Run(context.WithoutCancel(ctx), "api", "call", "block.close", mustJSON(map[string]string{"session_id": session, "block_id": block}))

	if input != nil {
		if wait {
			if err := h.waitFor(ctx, session, block, readyMarker); err != nil {
				return h.finish(ctx, session, block, err)
			}
		}
		data := base64.StdEncoding.EncodeToString(input) + endMarker
		if !wait {
			data = wrap76(base64.StdEncoding.EncodeToString(input)) + "\x04"
		}
		for len(data) > 0 {
			n := min(Chunk, len(data))
			var out map[string]any
			if err := h.Rex.Call(ctx, &out, session, block, "write", map[string]string{"data": data[:n]}); err != nil {
				return h.finish(ctx, session, block, fmt.Errorf("%w: writing input: %v", ErrUnknown, err))
			}
			data = data[n:]
		}
	}
	return h.finish(ctx, session, block, nil)
}

func (h Host) finish(ctx context.Context, session, block string, cause error) (int, string, error) {
	timeout := h.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p, err := h.Rex.Process(ctx, session, block)
		if err == nil && p.LastExit != nil {
			out, _ := h.Rex.Run(ctx, "capture", "-s", session, "-b", block, "--trim", "--unwrap")
			return p.LastExit.Code, string(out), cause
		}
		select {
		case <-ctx.Done():
			return 0, "", fmt.Errorf("%w: %v", ErrUnknown, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return 0, "", fmt.Errorf("%w: no exit after %s", ErrUnknown, timeout)
}

func (h Host) waitFor(ctx context.Context, session, block, marker string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out, err := h.Rex.Run(ctx, "capture", "-s", session, "-b", block, "--trim", "--unwrap")
		if err == nil && strings.Contains(string(out), marker) {
			return nil
		}
		if p, err := h.Rex.Process(ctx, session, block); err == nil && p.LastExit != nil {
			return nil // exited early; finish reports why
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%w: the remote command never became ready", ErrUnknown)
}

// session picks the session for a detached block: h.Session when set,
// else the first session the host lists. A host with none gets a windowless
// session that done destroys. A detached block shows in no window, so
// borrowing a session adds nothing to the operator's view.
func (h Host) session(ctx context.Context) (id string, done func(), err error) {
	ss, err := h.Rex.Sessions(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, s := range ss {
		if h.Session == "" || s.Label == h.Session {
			return s.ID, func() {}, nil
		}
	}
	label := cmp.Or(h.Session, InstallSession)
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := h.Rex.JSON(ctx, &out, "api", "call", "session.create", mustJSON(map[string]string{"label": label})); err != nil {
		return "", nil, err
	}
	if h.Session != "" {
		return out.SessionID, func() {}, nil // a named session is the caller's to keep
	}
	return out.SessionID, func() {
		h.Rex.Run(context.WithoutCancel(ctx), "api", "call", "session.destroy", mustJSON(map[string]string{"session_id": out.SessionID}))
	}, nil
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76] + "\n")
		s = s[76:]
	}
	return b.String() + s + "\n"
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vizkid/roar/internal/claude"
	"github.com/vizkid/roar/internal/rex"
)

// cmdMCP is the adapter Claude starts as its MCP server. It never fails
// loudly: without a Rex pane or the channel flag it serves MCP with no
// channel, and the pane falls back to terminal delivery.
func cmdMCP(ctx context.Context, env Env, _ []string) error {
	st, err := openStore(env)
	if err != nil {
		return err
	}
	rc := env.Rex
	if dir := env.Getenv("REX_BIN_DIR"); dir != "" {
		rc.Bin = filepath.Join(dir, "rex") // an MCP child may lack a login PATH
	}
	session, block := env.Getenv("REX_SESSION"), env.Getenv("REX_BLOCK")
	configDir := env.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		configDir = filepath.Join(env.Getenv("HOME"), ".claude")
	}
	ppid := os.Getppid()
	parentArgs := claude.ParentArgs
	if session == "" || block == "" {
		parentArgs = func(int) ([]string, error) { return nil, errNotInPane }
		block = "none"
	}
	cfg := claude.Config{
		Store: st, Session: session, Block: block, ParentPID: ppid,
		ParentArgs: parentArgs,
		Transcript: func() (string, error) { return claude.TranscriptPath(configDir, ppid) },
		AgentStatus: func(ctx context.Context) (claude.Status, error) {
			s, err := rc.ProgramStatus(ctx, session, block)
			if err != nil {
				return claude.Status{}, err
			}
			return claudeStatus(s, ppid)
		},
		Now:    env.Now,
		Writer: fmt.Sprintf("mcp:%d:%d", os.Getpid(), time.Now().UnixNano()),
		Log:    env.Stderr,
	}
	return claude.Serve(watchContext(ctx), cfg, env.Stdin, env.Stdout)
}

// claudeStatus accepts only the record of this adapter's own Claude: a
// stale or foreign record could report rest while Claude is mid-turn.
func claudeStatus(s *rex.StatusRecord, claudePID int) (claude.Status, error) {
	switch {
	case s == nil:
		return claude.Status{}, errors.New("no status record")
	case s.Owner == nil || s.Owner.PID != claudePID:
		return claude.Status{}, errors.New("status record from another process")
	case s.EffectiveApp != "claude-code":
		return claude.Status{}, fmt.Errorf("status record from %q", s.EffectiveApp)
	}
	return claude.Status{State: s.State, At: s.UpdatedAt}, nil
}

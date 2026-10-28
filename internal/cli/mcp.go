package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vizkid/roar/internal/claude"
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
		AgentStatus: func(ctx context.Context) (string, error) {
			s, err := rc.ProgramStatus(ctx, session, block)
			if err != nil || s == nil {
				return "", fmt.Errorf("no status: %v", err)
			}
			return s.State, nil
		},
		Now:    env.Now,
		Writer: fmt.Sprintf("mcp:%d:%d", os.Getpid(), time.Now().UnixNano()),
		Log:    env.Stderr,
	}
	return claude.Serve(watchContext(ctx), cfg, env.Stdin, env.Stdout)
}

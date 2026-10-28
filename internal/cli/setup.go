package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vizkid/roar/internal/setup"
	"github.com/vizkid/roar/internal/store"
)

// cmdSetup adds Roar to every Claude and Codex config root in $HOME.
func cmdSetup(_ context.Context, env Env, args []string) error {
	if len(args) != 0 {
		return usageErr("roar setup")
	}
	roar, err := os.Executable()
	if err != nil {
		return err
	}
	home := env.Getenv("HOME")
	roots := setup.Find(home, env.Getenv)
	if len(roots.Claude)+len(roots.Codex) == 0 {
		fmt.Fprintf(env.Stdout, "no Claude or Codex config roots in %s\n", home)
		return nil
	}
	err = setup.Run(env.Stdout, env.Stderr, home, roar, roots, claudeCLI(home))
	fmt.Fprint(env.Stdout, `Claude loads the Roar channel only when started with --dangerously-load-development-channels server:roar.
Codex runs the Roar hook only after you trust it: run /hooks once in each Codex home.
`)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return silentExit(exitRefused)
	}
	return nil
}

// claudeCLI runs the claude binary itself, never a shell function that
// wraps it. A detached Rex block's PATH lacks ~/.local/bin, where the
// native installer puts claude.
func claudeCLI(home string) setup.ClaudeCLI {
	return func(dir string, args ...string) (string, error) {
		bin, err := exec.LookPath("claude")
		if err != nil {
			bin = filepath.Join(home, ".local", "bin", "claude")
			if _, err := os.Stat(bin); err != nil {
				return "", errors.New("claude is not on PATH, so the MCP server was not registered")
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		if dir != "" {
			cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+dir)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("claude %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
}

// cmdHook is the Codex SessionStart hook. It records which thread the pane
// shows. It always exits 0 and prints nothing: Codex adds a SessionStart
// hook's output to the model's context, and a hook must never block Codex.
func cmdHook(ctx context.Context, env Env, args []string) error {
	if len(args) != 1 || args[0] != "codex" {
		return usageErr("roar hook codex")
	}
	session, block := env.Getenv("REX_SESSION"), env.Getenv("REX_BLOCK")
	var in struct {
		SessionID string `json:"session_id"`
	}
	if session == "" || block == "" || json.NewDecoder(env.Stdin).Decode(&in) != nil || in.SessionID == "" {
		return nil
	}
	home := env.Getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(env.Getenv("HOME"), ".codex")
	}
	rc := env.Rex
	if dir := env.Getenv("REX_BIN_DIR"); dir != "" {
		rc.Bin = filepath.Join(dir, "rex")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second) // Codex allows the hook 5 s
	defer cancel()
	p, err := rc.Process(ctx, session, block)
	if err != nil || p.Foreground == nil {
		return nil
	}
	st, err := openStore(env)
	if err != nil {
		return nil
	}
	st.BindCodex(block, store.CodexBinding{Thread: in.SessionID, CodexHome: home, PID: p.Foreground.PID, At: env.Now()})
	return nil
}

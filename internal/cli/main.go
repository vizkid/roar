// Package cli implements the roar commands.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/store"
)

// Env is everything a command touches outside its arguments, so tests can
// run commands in-process as if typed in a given pane.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Now    func() time.Time
	Rex    rex.Client
	// Self is this roar binary, which a watcher runs. Empty starts no watcher.
	Self string
	// Relay labels the session that holds the watcher's block; empty means
	// remote.RelaySession.
	Relay string
}

// Exit codes shared by every command.
const (
	exitOK      = 0
	exitWait    = 1 // only `roar ready`: not ready yet
	exitRefused = 2
)

type command func(ctx context.Context, env Env, args []string) error

var commands = map[string]command{
	"send":     cmdSend,
	"inbox":    cmdInbox,
	"read":     cmdRead,
	"status":   cmdStatus,
	"withdraw": cmdWithdraw,
	"resolve":  cmdResolve,
	"ready":    cmdReady,
	"deliver":  cmdDeliver,
	"mcp":      cmdMCP,
	"hook":     cmdHook,
	"bind":     cmdBind,
	"enqueue":  cmdEnqueue,
	"setup":    cmdSetup,
	"install":  cmdInstall,
}

const usage = `usage: roar <command> [args]

  send <target> [text]       store a message and try to deliver it (stdin when no text)
  inbox                      list messages waiting for this pane
  read <id> [--mark]         print a message; --mark marks it read
  status [id...]             show the state of messages you sent
  withdraw <id>              expire your own queued message
  resolve <id> --read|--requeue   settle a message in state unknown
  ready <target>             is the pane's agent at rest with an empty composer? (exit 0 yes, 1 wait, 2 never)
  deliver [target...] [--watch]   deliver held messages to panes that are ready
  setup                      add Roar to every Claude and Codex config root in $HOME
  install [--binary <path>] <host-label>   copy this roar, or another platform's build, to
                             ~/.local/bin on another Rex host, then run setup there
  enqueue --sha256 <digest>  receive a message from another host (Roar runs this itself)
  mcp                        the Claude adapter (Claude starts it; do not run it yourself)
  hook codex                 the Codex SessionStart hook (Codex runs it; do not run it yourself)
  bind agy                   in Antigravity: let Roar deliver to this session directly
`

// Main runs one command and returns its exit code.
func Main(args []string, env Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.Stdout, usage)
		return exitOK
	}
	// A help flag after a command must not reach it as a target or host.
	if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
		fmt.Fprint(env.Stdout, usage)
		return exitOK
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "roar: unknown_command: %q\n%s", args[0], usage)
		return exitRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := cmd(ctx, env, args[1:])
	var silent silentExit
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &silent):
		return int(silent)
	default:
		fmt.Fprintf(env.Stderr, "roar: %v\n", err)
		return exitRefused
	}
}

// watchContext drops the command deadline and ends on SIGINT or SIGTERM.
func watchContext(ctx context.Context) context.Context {
	ctx, _ = signal.NotifyContext(context.WithoutCancel(ctx), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func openStore(env Env) (*store.Store, error) {
	root := env.Getenv("ROAR_HOME")
	if root == "" {
		var err error
		if root, err = defaultRoot(env); err != nil {
			return nil, err
		}
	}
	return store.Open(root)
}

func defaultRoot(env Env) (string, error) {
	if v := env.Getenv("XDG_STATE_HOME"); v != "" {
		return v + "/roar", nil
	}
	if v := env.Getenv("HOME"); v != "" {
		return v + "/.local/state/roar", nil
	}
	return store.DefaultRoot()
}

// parse handles flags before, between, and after positional arguments.
// Everything after "--" is positional.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("usage: %v", err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func usageErr(format string, a ...any) error {
	return fmt.Errorf("usage: "+format, a...)
}

func joinText(words []string) string { return strings.Join(words, " ") }

// Package cli implements the ping commands.
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

	"github.com/vizkid/ping/internal/rex"
	"github.com/vizkid/ping/internal/store"
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
}

// Exit codes shared by every command.
const (
	exitOK      = 0
	exitWait    = 1 // only `ping ready`: not ready yet
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
}

const usage = `usage: ping <command> [args]

  send <target> [text]       store a message and try to deliver it (stdin when no text)
  inbox                      list messages waiting for this pane
  read <id> [--mark]         print a message; --mark marks it read
  status [id...]             show the state of messages you sent
  withdraw <id>              expire your own queued message
  resolve <id> --read|--requeue   settle a message in state unknown
  ready <target>             is the pane's agent at rest with an empty composer? (exit 0 yes, 1 wait, 2 never)
  deliver [target...] [--watch]   type held messages into panes that are ready
`

// Main runs one command and returns its exit code.
func Main(args []string, env Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.Stdout, usage)
		return exitOK
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "ping: unknown_command: %q\n%s", args[0], usage)
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
		fmt.Fprintf(env.Stderr, "ping: %v\n", err)
		return exitRefused
	}
}

// watchContext drops the command deadline and ends on SIGINT or SIGTERM.
func watchContext(ctx context.Context) context.Context {
	ctx, _ = signal.NotifyContext(context.WithoutCancel(ctx), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func openStore(env Env) (*store.Store, error) {
	root := env.Getenv("PING_HOME")
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
		return v + "/ping", nil
	}
	if v := env.Getenv("HOME"); v != "" {
		return v + "/.local/state/ping", nil
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

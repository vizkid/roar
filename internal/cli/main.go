// Package cli implements the ping commands.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
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
}

const usage = `usage: ping <command> [args]

  send <target> [text]       store a message and try to deliver it (stdin when no text)
  inbox                      list messages waiting for this pane
  read <id> [--mark]         print a message; --mark marks it read
  status [id...]             show the state of messages you sent
  withdraw <id>              expire your own queued message
  resolve <id> --read|--requeue   settle a message in state unknown
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
	var wait waitError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &wait):
		fmt.Fprintln(env.Stdout, wait.msg)
		return exitWait
	default:
		fmt.Fprintf(env.Stderr, "ping: %v\n", err)
		return exitRefused
	}
}

// waitError is a "not yet" outcome, not a failure.
type waitError struct{ msg string }

func (w waitError) Error() string { return w.msg }

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

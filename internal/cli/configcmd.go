package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/vizkid/roar/internal/config"
)

const configUsage = "roar config [get <key> | set <key> <value> | unset <key> | profile <name> | check] [--host <label>]"

// cmdConfig shows and edits the settings in docs/ROARD.md "Configuration".
// Edits keep the file's other lines and comments.
func cmdConfig(_ context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	host := fs.String("host", "", "the [hosts.<label>] section, or the settings for talking to that host")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	path := config.DefaultPath(env.Getenv, env.Getenv("HOME"))
	verb := ""
	if len(pos) > 0 {
		verb, pos = pos[0], pos[1:]
	}
	want := map[string]int{"": 0, "get": 1, "set": 2, "unset": 1, "profile": 1, "check": 0}
	if n, ok := want[verb]; !ok || len(pos) != n {
		return usageErr(configUsage)
	}
	switch verb {
	case "set":
		return config.Set(path, *host, pos[0], pos[1])
	case "unset":
		return config.Unset(path, *host, pos[0])
	case "profile":
		return config.SetProfile(path, *host, pos[0])
	}
	s, warn, err := config.Load(config.FromEnv(env.Getenv, env.Getenv("HOME"), *host))
	for _, w := range warn {
		fmt.Fprintf(env.Stderr, "warning: %s\n", w)
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "error: %v\n", err)
		return silentExit(exitRefused)
	}
	switch verb {
	case "get":
		if _, ok := config.Lookup(pos[0]); !ok {
			return fmt.Errorf("unknown key %s", pos[0])
		}
		fmt.Fprintln(env.Stdout, s.Value(pos[0]))
	case "check":
		fmt.Fprintf(env.Stdout, "%s: ok, %d warnings\n", path, len(warn))
	default:
		fmt.Fprintf(env.Stdout, "# %s\n", path)
		for _, k := range config.Keys() {
			fmt.Fprintf(env.Stdout, "%-34s %-10s %s\n", k.Name, s.Value(k.Name), s.Source(k.Name))
		}
	}
	return nil
}

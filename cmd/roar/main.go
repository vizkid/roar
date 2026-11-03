// Command roar routes messages between Rex panes.
package main

import (
	"os"
	"time"

	"github.com/vizkid/rexkit/rex"
	"github.com/vizkid/roar/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.Env{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
		Now:    time.Now,
		Rex:    rex.Local(),
	}))
}

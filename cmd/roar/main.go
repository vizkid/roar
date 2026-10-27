// Command roar routes messages between Rex panes.
package main

import (
	"os"
	"time"

	"github.com/vizkid/roar/internal/cli"
	"github.com/vizkid/roar/internal/rex"
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

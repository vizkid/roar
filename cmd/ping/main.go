// Command ping routes messages between Rex panes.
package main

import (
	"os"
	"time"

	"github.com/vizkid/ping/internal/cli"
	"github.com/vizkid/ping/internal/rex"
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

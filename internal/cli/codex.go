package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/vizkid/ping/internal/codex"
)

// cmdCodex runs Codex behind Ping's proxy and delivers this pane's messages
// as native turns. It exits with the TUI's exit code.
func cmdCodex(ctx context.Context, env Env, args []string) error {
	bin := env.Getenv("PING_CODEX_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("codex"); err != nil {
			return errors.New("no_codex: codex is not on PATH; set PING_CODEX_BIN")
		}
	}
	ctx = context.WithoutCancel(ctx) // the TUI may run for days
	sess, err := codex.Start(ctx, bin)
	if err != nil {
		return err
	}
	defer sess.Stop()
	// A pane hangup or kill must not leave the App Server running.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(hup)
	go func() {
		<-hup
		sess.Stop()
		os.Exit(129)
	}()

	if block := env.Getenv("REX_BLOCK"); block != "" {
		st, err := openStore(env)
		if err != nil {
			return err
		}
		lock, err := st.HoldAdapter(block)
		if err != nil {
			fmt.Fprintf(env.Stderr, "ping codex: another adapter serves this pane; messages will not be delivered here: %v\n", err)
		} else {
			defer lock.Release()
			actx, cancel := context.WithCancel(ctx)
			defer cancel()
			a := &codex.Adapter{Store: st, Block: block, Proxy: sess.Proxy, Now: env.Now, Log: env.Stderr,
				Writer: fmt.Sprintf("codex:%d:%d", os.Getpid(), time.Now().UnixNano())}
			go a.Run(actx)
		}
	}

	tui := exec.Command(bin, append([]string{"--remote", "unix://" + sess.ProxySock}, args...)...)
	tui.Stdin, tui.Stdout, tui.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Like a shell: terminal signals are for the TUI, and Ping must outlive
	// it to stop the App Server.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	err = tui.Run()
	signal.Reset(syscall.SIGINT, syscall.SIGQUIT)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		sess.Stop()
		return silentExit(ee.ExitCode())
	}
	return err
}

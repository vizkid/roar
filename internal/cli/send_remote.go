package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/vizkid/rexkit/rex"
	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/remote"
)

// splitHost separates a "host-label:" prefix that names a Rex host. Block
// and session IDs also contain a colon, so only a known host label, this
// machine's own name, or a DNS name counts. A remote label wins over this
// machine's name.
func splitHost(ctx context.Context, c rex.Client, target string) (host, rest string, err error) {
	label, rest, ok := strings.Cut(target, ":")
	if !ok || label == "block" || label == "session" {
		return "", target, nil
	}
	h, found, err := storedHost(rexHosts(ctx, c), label)
	if err != nil {
		return "", "", err
	}
	if found {
		return h.Label, rest, nil
	}
	// Rex gives this host an empty label, but other hosts know it by its
	// machine name, so an address copied from their messages carries it.
	if strings.EqualFold(label, localName()) {
		return "", rest, nil
	}
	// A host this Rex has no label for can be named by its DNS name, as
	// `roar install` takes it. No pane address has a dot before its colon.
	if strings.Contains(label, ".") {
		return label, rest, nil
	}
	if name := tailnetHost(ctx, c, label); name != "" {
		return name, rest, nil
	}
	return "", target, nil
}

// unknownHost replaces no_such_target for a "<host>:" prefix that named
// nothing. Read as a pane label it would look like an unreachable host.
func unknownHost(raw string) error {
	label, _, ok := strings.Cut(raw, ":")
	if !ok || label == "block" || label == "session" {
		return nil
	}
	return hostError(label)
}

func hostError(label string) error {
	return fmt.Errorf("unknown_host: Rex here has no host %q and no tailnet name for it; add one with: rex hosts add %s <endpoint>", label, label)
}

// localName is this machine's host name without its domain: "Stiletto" for
// "Stiletto.local".
func localName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	name, _, _ = strings.Cut(name, ".")
	return name
}

// installHost is the Host install uses, with the target host's install.*
// settings.
func installHost(env Env, label string) (remote.Host, *config.Settings, error) {
	s, err := loadSettings(env, label)
	if err != nil {
		return remote.Host{}, nil, err
	}
	h := remoteHost(env, label)
	h.Timeout, h.Poll = s.Duration("install.deadline"), s.Duration("install.ready_check")
	return h, s, nil
}

// remoteHost is the Host for a server label or endpoint.
func remoteHost(env Env, server string) remote.Host {
	c := env.Rex
	c.Server = server
	return remote.Host{Rex: c, Bin: env.Getenv("ROAR_REMOTE_BIN")}
}

// sendRemote hands the envelope to roard on the recipient's host and
// returns the first settled record. Peer retries, resumes and backs off;
// once the message may have landed, a failure is ErrUnknown.
func sendRemote(ctx context.Context, env Env, e envelope.Envelope, files map[string][]byte) (remote.Result, error) {
	server := e.To.Endpoint
	if server == "" {
		return remote.Result{}, errors.New("no_route: the recipient has no Rex endpoint")
	}
	p, err := peerFor(ctx, env, server)
	if err != nil {
		return remote.Result{}, err
	}
	// The send's own budget replaces the two-minute command deadline, which
	// a slow link can need more than. Ctrl-C and SIGTERM still end it.
	sctx, stop := signal.NotifyContext(context.WithoutCancel(ctx), os.Interrupt, syscall.SIGTERM)
	defer stop()
	row, err := p.Send(sctx, e, files)
	saveRTT(env, p)
	if errors.Is(err, remote.ErrNoRoard) {
		return remote.Result{}, fmt.Errorf("roard_not_running: no roard answers on %s; roar install sets it up", server)
	}
	if err != nil && !errors.Is(err, remote.ErrUnknown) {
		return remote.Result{}, err
	}
	return remote.Result{ID: e.ID, State: row.State, Reason: cmp.Or(row.HoldReason, row.Reason)}, err
}

// cmdInstall copies this roar binary, or the --binary build for another
// platform, to a Rex host's ~/.local/bin, then runs roar setup there.
func cmdInstall(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	path := fs.String("binary", "", "the roar build to copy")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("roar install [--binary <path>] <host-label>")
	}
	if *path == "" {
		if *path, err = os.Executable(); err != nil {
			return err
		}
	}
	bin, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	h, s, err := installHost(env, pos[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.Duration("install.deadline"))
	defer cancel()
	if err := h.Install(ctx, bin); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "installed at %s on %s\n", remote.DefaultBin, pos[0])
	out, err := h.Setup(ctx)
	if out != "" {
		fmt.Fprintln(env.Stdout, out)
	}
	return err
}

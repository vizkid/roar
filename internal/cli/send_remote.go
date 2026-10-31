package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/remote"
	"github.com/vizkid/roar/internal/rex"
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
	var out struct {
		Hosts []struct {
			Host struct {
				Label string `json:"label"`
			} `json:"host"`
		} `json:"hosts"`
	}
	if err := c.JSON(ctx, &out, "hosts", "--json"); err == nil {
		for _, h := range out.Hosts {
			if strings.EqualFold(h.Host.Label, label) {
				return h.Host.Label, rest, nil
			}
		}
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
	return "", target, nil
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

// remoteHost is the Host for a server label or endpoint.
func remoteHost(env Env, server string) remote.Host {
	c := env.Rex
	c.Server = server
	return remote.Host{Rex: c, Bin: env.Getenv("ROAR_REMOTE_BIN")}
}

// sendRemote hands the envelope to the recipient's host. A lost answer is
// retried with the same envelope; the receiver dedupes by ID. Once one try
// may have landed, every later failure leaves the outcome unknown.
func sendRemote(ctx context.Context, env Env, e envelope.Envelope, files map[string][]byte) (remote.Result, error) {
	server := e.To.Endpoint
	if server == "" {
		return remote.Result{}, errors.New("no_route: the recipient has no Rex endpoint")
	}
	h := remoteHost(env, server)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendBudget(files))
	defer cancel()
	var unknown error
	for range 3 {
		r, err := h.EnqueueWithFiles(ctx, e, files)
		switch {
		case err == nil:
			return r, nil
		case errors.Is(err, remote.ErrUnknown):
			unknown = err
		case unknown == nil:
			return r, err
		}
		if ctx.Err() != nil {
			break // a retry would fail at its first rex call
		}
	}
	return remote.Result{}, unknown
}

// sendBudget is how long a remote send may take. Rex takes the payload in
// Chunk-sized writes, one rex call each, and a loaded host needs about a
// second per call. A 12 MiB attachment then takes minutes, more than the
// two every command gets.
func sendBudget(files map[string][]byte) time.Duration {
	size := 0
	for _, b := range files {
		size += base64.StdEncoding.EncodedLen(len(b))
	}
	return 2*time.Minute + time.Duration(size/remote.Chunk)*time.Second
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
	h := remoteHost(env, pos[0])
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

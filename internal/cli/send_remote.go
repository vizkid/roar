package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/remote"
	"github.com/vizkid/roar/internal/rex"
)

// splitHost separates a "host-label:" prefix that names a Rex host. Block
// and session IDs also contain a colon, so only a known host label counts.
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
	if err := c.JSON(ctx, &out, "hosts", "--json"); err != nil {
		return "", target, nil // no host list: treat the target as local
	}
	for _, h := range out.Hosts {
		if strings.EqualFold(h.Host.Label, label) {
			return h.Host.Label, rest, nil
		}
	}
	return "", target, nil
}

// remoteHost is the Host for a server label or endpoint.
func remoteHost(env Env, server string) remote.Host {
	c := env.Rex
	c.Server = server
	return remote.Host{Rex: c, Bin: env.Getenv("ROAR_REMOTE_BIN")}
}

// sendRemote hands the envelope to the recipient's host. A lost answer is
// retried with the same envelope; the receiver dedupes by ID.
func sendRemote(ctx context.Context, env Env, e envelope.Envelope, files map[string][]byte) (remote.Result, error) {
	server := e.To.Endpoint
	if server == "" {
		return remote.Result{}, errors.New("no_route: the recipient has no Rex endpoint")
	}
	h := remoteHost(env, server)
	var lastErr error
	for range 3 {
		r, err := h.EnqueueWithFiles(ctx, e, files)
		if err == nil || !errors.Is(err, remote.ErrUnknown) {
			return r, err
		}
		lastErr = err
	}
	return remote.Result{}, lastErr
}

// cmdInstall copies this roar binary to a Rex host's ~/.local/bin.
func cmdInstall(ctx context.Context, env Env, args []string) error {
	if len(args) != 1 {
		return usageErr("roar install <host-label>")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if err := remoteHost(env, args[0]).Install(ctx, bin); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "installed at %s on %s\n", remote.DefaultBin, args[0])
	return nil
}

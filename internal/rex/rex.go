// Package rex wraps the rex CLI. Ping uses the CLI's JSON output rather than
// Rex's wire protocol, so it keeps working across Rex versions that keep the CLI.
package rex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotFound means Rex reported the target missing (CLI exit 3).
var ErrNotFound = errors.New("rex target not found")

// Client runs rex against one server. Server is empty for the local server,
// or a host label or endpoint passed as -S.
type Client struct {
	Bin    string
	Server string
}

// Local is the local server, using rex from PATH.
func Local() Client { return Client{Bin: "rex"} }

// Run calls rex and returns stdout.
func (c Client) Run(ctx context.Context, args ...string) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "rex"
	}
	full := args
	if c.Server != "" {
		full = append([]string{"-S", c.Server}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, msg)
		}
		return nil, fmt.Errorf("rex %s: %v: %s", strings.Join(args[:min(2, len(args))], " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// JSON calls rex and decodes its stdout into v.
func (c Client) JSON(ctx context.Context, v any, args ...string) error {
	out, err := c.Run(ctx, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("rex %s: decoding output: %w", args[0], err)
	}
	return nil
}

// Whoami describes the server and, inside a pane, the caller's location.
type Whoami struct {
	Endpoint   string `json:"endpoint"`
	InstanceID string `json:"instance_id"`
	HostID     string `json:"host_id"`
	SessionID  string `json:"session_id"`
	BlockID    string `json:"block_id"`
	Tailscale  struct {
		HTTPS struct {
			URL string `json:"url"`
		} `json:"https"`
	} `json:"tailscale"`
}

func (c Client) Whoami(ctx context.Context) (Whoami, error) {
	var w Whoami
	return w, c.JSON(ctx, &w, "whoami", "--json")
}

// Session is one entry of `rex ls`.
type Session struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

func (c Client) Sessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	return out.Sessions, c.JSON(ctx, &out, "ls", "--json")
}

// Block is one entry of `rex block ls`.
type Block struct {
	ID        string `json:"block_id"`
	Label     string `json:"label"`
	Flavor    string `json:"flavor"`
	HostID    string `json:"host_id"`
	Placement string `json:"placement"`
}

// Terminal reports whether the block is a placed terminal pane.
func (b Block) Terminal() bool {
	return b.Placement == "placed" && strings.HasPrefix(b.Flavor, "com.superlogical.terminal")
}

func (c Client) Blocks(ctx context.Context, session string) ([]Block, error) {
	var out struct {
		Blocks []Block `json:"blocks"`
	}
	return out.Blocks, c.JSON(ctx, &out, "block", "ls", "-s", session, "--json")
}

// Call invokes a block method and decodes its result.
func (c Client) Call(ctx context.Context, v any, session, block, method string, args any) error {
	in := []byte("{}")
	if args != nil {
		var err error
		if in, err = json.Marshal(args); err != nil {
			return err
		}
	}
	return c.JSON(ctx, v, "block", "call", "-s", session, "-b", block, method, string(in))
}

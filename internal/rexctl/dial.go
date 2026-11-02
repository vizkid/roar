package rexctl

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// LocalEndpoint is this host's own server: REX_SERVER, else Rex's default
// socket for the platform.
func LocalEndpoint(getenv func(string) string, home string) string {
	if s := getenv("REX_SERVER"); s != "" {
		return s
	}
	if runtime.GOOS == "darwin" {
		return "unix://" + filepath.Join(home, "Library", "Application Support", "rex", "server.sock")
	}
	dir := getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "rex-"+strconv.Itoa(os.Getuid()))
	}
	return "unix://" + filepath.Join(dir, "rex", "server.sock")
}

func dialer(endpoint string) (*websocket.Dialer, string, error) {
	d := &websocket.Dialer{Subprotocols: []string{protocol}, HandshakeTimeout: 0}
	if path, ok := strings.CutPrefix(endpoint, "unix://"); ok {
		p, err := url.PathUnescape(path)
		if err != nil {
			return nil, "", fmt.Errorf("rex endpoint %q: %w", endpoint, err)
		}
		d.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", p)
		}
		return d, "ws://localhost/api/control", nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, "", fmt.Errorf("rex endpoint %q: want unix://, https:// or http://", endpoint)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, "", fmt.Errorf("rex endpoint %q: want unix://, https:// or http://", endpoint)
	}
	u.Path = "/api/control"
	return d, u.String(), nil
}

// Dial opens the connection, sends the hello, and waits for the welcome.
// ctx bounds only the opening.
func Dial(ctx context.Context, o Options) (*Client, error) {
	d, target, err := dialer(o.Endpoint)
	if err != nil {
		return nil, err
	}
	conn, resp, err := d.DialContext(ctx, target, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("rex control %s: %w", o.Endpoint, err)
	}
	if conn.Subprotocol() != protocol {
		conn.Close()
		return nil, fmt.Errorf("rex control %s: server chose subprotocol %q", o.Endpoint, conn.Subprotocol())
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(dl)
		conn.SetWriteDeadline(dl)
	}
	hello := map[string]any{"type": "hello", "api_revision": apiRevision, "api_min_revision": apiRevision, "client": o.Info}
	if o.ReconnectToken != "" {
		hello["reconnect_token"] = o.ReconnectToken
	}
	if err := conn.WriteJSON(hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("rex control hello: %w", err)
	}
	var w struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
		Limits   struct {
			Max int `json:"max_message_bytes"`
		} `json:"limits"`
		Client struct {
			ID    string `json:"id"`
			Token string `json:"reconnect_token"`
		} `json:"client"`
	}
	if err := conn.ReadJSON(&w); err != nil || w.Type != "welcome" || w.Protocol != protocol {
		conn.Close()
		return nil, fmt.Errorf("rex control welcome: %v (type %q protocol %q)", err, w.Type, w.Protocol)
	}
	conn.SetReadDeadline(time.Time{})
	conn.SetWriteDeadline(time.Time{})
	c := &Client{conn: conn, id: w.Client.ID, token: w.Client.Token, max: max(w.Limits.Max, 64<<10),
		opts: o, pending: map[string]chan response{}, done: make(chan struct{})}
	conn.SetReadLimit(int64(c.max) + 4096)
	go c.read()
	return c, nil
}

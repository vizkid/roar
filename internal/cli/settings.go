package cli

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/vizkid/roar/internal/agy"
	"github.com/vizkid/roar/internal/claude"
	"github.com/vizkid/roar/internal/codex"
	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/fallback"
	"github.com/vizkid/roar/internal/remote"
)

// loadSettings reads the settings for a command that talks to host, or to
// no other host when host is empty. Warnings go to stderr.
func loadSettings(env Env, host string) (*config.Settings, error) {
	s, warn, err := config.Load(config.FromEnv(env.Getenv, env.Getenv("HOME"), host))
	for _, w := range warn {
		fmt.Fprintf(env.Stderr, "roar: config: %s\n", w)
	}
	return s, err
}

// rexHost is one host in `rex hosts --json`.
type rexHost struct {
	Label     string
	Endpoints []string
}

func rexHosts(ctx context.Context, env Env) []rexHost {
	var out struct {
		Hosts []struct {
			Host struct {
				Label     string   `json:"label"`
				Endpoints []string `json:"endpoints"`
			} `json:"host"`
		} `json:"hosts"`
	}
	if env.Rex.JSON(ctx, &out, "hosts", "--json") != nil {
		return nil
	}
	hosts := make([]rexHost, 0, len(out.Hosts))
	for _, h := range out.Hosts {
		hosts = append(hosts, rexHost{Label: h.Host.Label, Endpoints: h.Host.Endpoints})
	}
	return hosts
}

// resolveServer turns a host label or endpoint into an endpoint URL and the
// host label that settings use for it.
func resolveServer(ctx context.Context, env Env, server string) (endpoint, label string, err error) {
	hosts := rexHosts(ctx, env)
	if strings.Contains(server, "://") {
		for _, h := range hosts {
			for _, ep := range h.Endpoints {
				if strings.TrimSuffix(ep, "/") == strings.TrimSuffix(server, "/") {
					return server, h.Label, nil
				}
			}
		}
		return server, "", nil
	}
	for _, h := range hosts {
		if strings.EqualFold(h.Label, server) && len(h.Endpoints) > 0 {
			return h.Endpoints[0], h.Label, nil
		}
	}
	if strings.Contains(server, ".") { // a DNS name, as roar install takes it
		return "https://" + server, server, nil
	}
	if name := tailnetHost(ctx, env.Rex, server); name != "" {
		return "https://" + name, name, nil
	}
	return "", "", hostError(server)
}

// peerFor is the roard client for a host label or endpoint, with that
// host's settings.
func peerFor(ctx context.Context, env Env, server string) (*remote.Peer, error) {
	endpoint, label, err := resolveServer(ctx, env, server)
	if err != nil {
		return nil, err
	}
	s, err := loadSettings(env, label)
	if err != nil {
		return nil, err
	}
	p := &remote.Peer{Endpoint: endpoint, Label: env.Getenv("ROAR_ROARD_LABEL"), Settings: s}
	if f, ok := rttFile(env, endpoint); ok {
		if b, err := os.ReadFile(f); err == nil {
			if d, err := time.ParseDuration(strings.TrimSpace(string(b))); err == nil {
				p.SeedRTT(d) // the last command's round trip to this host
			}
		}
	}
	return p, nil
}

// rttFile keeps each host's smoothed round trip between commands, so the
// first deadlines of the next one already fit a slow link.
func rttFile(env Env, endpoint string) (string, bool) {
	st, err := openStore(env)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(endpoint))
	return filepath.Join(st.Root(), "rtt", hex.EncodeToString(sum[:8])), true
}

// saveRTT records what this command measured.
func saveRTT(env Env, p *remote.Peer) {
	f, ok := rttFile(env, p.Endpoint)
	if !ok || p.SRTT() == 0 {
		return
	}
	if os.MkdirAll(filepath.Dir(f), 0o700) == nil {
		os.WriteFile(f, []byte(p.SRTT().String()+"\n"), 0o600)
	}
}

// version is this build's VCS revision, for roard's hello.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value[:min(12, len(s.Value))]
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	return cmp.Or(rev, info.Main.Version, "unknown") + dirty
}

// timings are the settings in force for this command's own host: the
// defaults until applyTimings reads the file.
var timings, _, _ = config.Load(config.Options{})

// applyTimings reads the settings once per command and sets the delivery
// timers inherited from before the config file existed. A bad file stops
// the command, except roar config, which must be able to fix it, and the
// Codex hook, which must never fail Codex.
func applyTimings(env Env, command string) error {
	s, err := loadSettings(env, "")
	if err != nil {
		if command != "config" && command != "hook" {
			return err
		}
		s, _, _ = config.Load(config.Options{})
	}
	timings = s
	fallback.PasteWait = s.Duration("delivery.paste_wait")
	fallback.PasteCheck = s.Duration("delivery.paste_check")
	fallback.SubmitWait = s.Duration("delivery.submit_wait")
	claude.RequestGap = s.Duration("claude.request_gap")
	codex.DeliverTimeout = s.Duration("codex.deliver_timeout")
	agy.DeliverTimeout = s.Duration("agy.deliver_timeout")
	return nil
}

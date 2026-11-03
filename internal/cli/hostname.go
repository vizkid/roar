package cli

import (
	"context"
	"encoding/json"
	"net"
	"strings"

	"github.com/vizkid/rexkit/rex"
)

// lookupHost is a DNS lookup, replaced in tests.
var lookupHost = func(ctx context.Context, name string) error {
	_, err := net.DefaultResolver.LookupHost(ctx, name)
	return err
}

// tailnetHost finds a host Rex has no label for by its tailnet name. Stored
// host lists differ per machine, while every Rex on one tailnet is named the
// same way, so this host's own name gives the pattern. It returns "" when
// this Rex is not on a tailnet or the name does not exist.
func tailnetHost(ctx context.Context, c rex.Client, label string) string {
	var st struct {
		Tailscale struct {
			DNSName string `json:"dns_name"`
		} `json:"tailscale"`
	}
	out, err := c.Run(ctx, "api", "call", "server.status")
	if err != nil || json.Unmarshal(out, &st) != nil {
		return ""
	}
	name := tailnetCandidate(st.Tailscale.DNSName, localName(), label)
	if name == "" {
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, timings.Duration("target.dns_timeout"))
	defer cancel()
	if lookupHost(lctx, name) != nil {
		return ""
	}
	return name
}

// tailnetCandidate applies this host's name to label: on
// colonelk-rex.tail1.ts.net, Baroni is baroni-rex.tail1.ts.net.
func tailnetCandidate(dnsName, local, label string) string {
	node, domain, ok := strings.Cut(strings.TrimSuffix(dnsName, "."), ".")
	if !ok {
		return ""
	}
	suffix, _ := strings.CutPrefix(strings.ToLower(node), strings.ToLower(local))
	if suffix == strings.ToLower(node) {
		suffix = ""
	}
	return strings.ToLower(label) + suffix + "." + domain
}

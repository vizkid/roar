package cli

import (
	"fmt"
	"net/url"
	"strings"
)

// storedHost prefers exact labels, then unique short DNS names.
func storedHost(hosts []rexHost, name string) (rexHost, bool, error) {
	for _, h := range hosts {
		if strings.EqualFold(h.Label, name) {
			return h, true, nil
		}
	}
	var matches []rexHost
	for _, h := range hosts {
		names := []string{h.Label}
		for _, ep := range h.Endpoints {
			u, err := url.Parse(ep)
			if err == nil && u.Hostname() != "" {
				names = append(names, u.Hostname())
			}
		}
		for _, candidate := range names {
			short, _, _ := strings.Cut(candidate, ".")
			if strings.EqualFold(name, short) || strings.EqualFold(name, strings.TrimSuffix(strings.ToLower(short), "-rex")) {
				matches = append(matches, h)
				break
			}
		}
	}
	if len(matches) > 1 {
		labels := make([]string, len(matches))
		for i := range matches {
			labels[i] = matches[i].Label
		}
		return rexHost{}, false, fmt.Errorf("ambiguous_host: %q matches %s; use a stored host label", name, strings.Join(labels, ", "))
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	return rexHost{}, false, nil
}

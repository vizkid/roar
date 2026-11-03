package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vizkid/rexkit/rex"
)

// elsewhereWait bounds the search of other hosts. It runs only after a send
// has already failed, so it costs nothing on the normal path.
const elsewhereWait = 5 * time.Second

// otherHosts lists the labels of the Rex hosts this Rex knows, replaced in
// tests.
var otherHosts = hostLabels

// resolveElsewhere is rex.Resolve on another host, replaced in tests.
var resolveElsewhere = func(ctx context.Context, c rex.Client, host, target string) error {
	c.Server = host
	_, err := rex.Resolve(ctx, c, target)
	return err
}

// elsewhere adds to a local no_such_target the other hosts where target
// names a pane. Two hosts often have a session with the same label, so a
// local miss reads as "no such agent" and the sender gives up or guesses.
// It names the hosts and does not send there itself: the same label on two
// hosts may be two different agents, and only the sender knows which it
// meant. A host that does not answer in time is left out.
func elsewhere(ctx context.Context, c rex.Client, target string, miss error) error {
	hosts := otherHosts(ctx, c)
	if len(hosts) == 0 {
		return miss
	}
	ctx, cancel := context.WithTimeout(ctx, elsewhereWait)
	defer cancel()
	found := make([]string, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Go(func() {
			err := resolveElsewhere(ctx, c, h, target)
			switch {
			case err == nil:
				found[i] = h + ":" + target
			case errors.Is(err, rex.ErrAmbiguous):
				found[i] = fmt.Sprintf("%s:%s (%v)", h, target, err)
			}
		})
	}
	wg.Wait()
	found = slices.DeleteFunc(found, func(s string) bool { return s == "" })
	if len(found) == 0 {
		return miss
	}
	return fmt.Errorf("%w; on another host: %s", miss, strings.Join(found, ", "))
}

// hostLabels is the label of every host in `rex hosts`, without this one.
func hostLabels(ctx context.Context, c rex.Client) []string {
	var out struct {
		Hosts []struct {
			Host struct {
				Label string `json:"label"`
			} `json:"host"`
		} `json:"hosts"`
	}
	if c.JSON(ctx, &out, "hosts", "--json") != nil {
		return nil
	}
	var labels []string
	for _, h := range out.Hosts {
		if h.Host.Label != "" {
			labels = append(labels, h.Host.Label)
		}
	}
	return labels
}

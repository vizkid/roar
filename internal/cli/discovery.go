package cli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vizkid/rexkit/rex"
	"github.com/vizkid/roar/internal/envelope"
)

// printTarget reads labels from the resolved block without touching delivery.
func printTarget(ctx context.Context, env Env, rc rex.Client, to envelope.Address, asJSON bool) error {
	blocks, err := rc.Blocks(ctx, to.Session)
	if err != nil {
		return err
	}
	windows, err := rc.Windows(ctx, to.Session)
	if err != nil {
		return err
	}
	for _, b := range blocks {
		if b.ID != to.Block {
			continue
		}
		result := struct {
			envelope.Address
			Tab    string `json:"tab"`
			Pane   string `json:"pane"`
			Window string `json:"window"`
		}{to, tabName(b, windows), b.Label, b.WindowID}
		if asJSON {
			return writeJSON(env, result)
		}
		fmt.Fprintf(env.Stdout, "host: %s\nendpoint: %s\nsession: %s (%s)\ntab: %s (%s)\npane: %s (%s)\n",
			to.HostID, to.Endpoint, to.Label, to.Session, result.Tab, b.WindowID, b.Label, b.ID)
		return nil
	}
	return fmt.Errorf("%w: resolved block %s closed", rex.ErrNoTarget, to.Block)
}

// allPanes keeps partial results and reports each host that failed.
func allPanes(ctx context.Context, env Env) ([]paneRow, error) {
	rows, err := localPanes(ctx, env)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Host = localName()
	}
	hosts := hostLabels(ctx, localRex(env))
	results := make([][]paneRow, len(hosts))
	errs := make([]error, len(hosts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, host := range hosts {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			results[i], errs[i] = remotePanes(hctx, env, host)
			for j := range results[i] {
				results[i][j].Host = host
			}
		})
	}
	wg.Wait()
	for i := range hosts {
		if errs[i] != nil {
			fmt.Fprintf(env.Stderr, "roar: panes %s: %v\n", hosts[i], errs[i])
			continue
		}
		rows = append(rows, results[i]...)
	}
	return rows, nil
}

package rexctl

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Declare sets this client's label, unless it is empty, and lists the
// actions it offers.
func (c *Client) Declare(ctx context.Context, label string, actions []Action) error {
	if label != "" {
		if err := c.Call(ctx, "client.set_label", map[string]string{"label": label}, nil); err != nil {
			return err
		}
	}
	list := make([]map[string]any, len(actions))
	for i, a := range actions {
		m := map[string]any{"name": a.Name, "kind": "client"}
		if a.Title != "" {
			m["title"] = a.Title
		}
		if a.Description != "" {
			m["description"] = a.Description
		}
		if a.Args != nil {
			m["args"] = a.Args
		}
		list[i] = m
	}
	return c.Call(ctx, "client.keymap", map[string]any{"actions": list, "server_actions": "none"}, nil)
}

// Resolve finds the one client with this label.
func (c *Client) Resolve(ctx context.Context, label string) (string, error) {
	var out struct {
		Results []struct {
			Status string `json:"status"`
			Target struct {
				ClientID string `json:"client_id"`
			} `json:"target"`
		} `json:"results"`
	}
	if err := c.Call(ctx, "target.resolve", map[string]any{"queries": []string{label}, "kinds": []string{"client"}}, &out); err != nil {
		return "", err
	}
	if len(out.Results) != 1 {
		return "", fmt.Errorf("target.resolve: %d results", len(out.Results))
	}
	switch r := out.Results[0]; r.Status {
	case "resolved":
		return r.Target.ClientID, nil
	case "ambiguous":
		return "", ErrAmbiguous
	default:
		return "", ErrNoClient
	}
}

// Perform calls another client's action. timeout is Rex's timeout_ms: how
// long that client may take once the request reaches the front of its
// queue, at most 30s. ctx bounds the caller's whole wait, which also covers
// the network and the queue.
func (c *Client) Perform(ctx context.Context, clientID, action string, args any, timeout time.Duration, out any) error {
	wait, err := c.StartPerform(clientID, action, args, timeout)
	if err != nil {
		return err
	}
	return wait(ctx, out)
}

// StartPerform sends a Perform and returns a function that waits for it.
func (c *Client) StartPerform(clientID, action string, args any, timeout time.Duration) (func(ctx context.Context, out any) error, error) {
	req := map[string]any{"client_id": clientID, "action": action, "timeout_ms": min(timeout, 30*time.Second).Milliseconds()}
	if args != nil {
		req["args"] = args
	}
	wait, err := c.Start("client.perform", req)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, out any) error {
		var res struct {
			Result json.RawMessage `json:"result"`
		}
		if err := wait(ctx, &res); err != nil {
			return err
		}
		if out != nil && len(res.Result) > 0 {
			return json.Unmarshal(res.Result, out)
		}
		return nil
	}, nil
}

// perform answers one request with OnPerform's result. A client with no
// handler declines every action.
func (c *Client) perform(p Perform) {
	var result any
	var perr *Error
	if c.opts.OnPerform == nil {
		perr = &Error{Code: "not_supported", Message: "this client offers no actions"}
	} else {
		result, perr = c.opts.OnPerform(p)
	}
	reply := map[string]any{"call_id": p.CallID}
	if perr != nil {
		reply["error"] = perr
	} else {
		reply["result"] = result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.Call(ctx, "client.reply", reply, nil)
}

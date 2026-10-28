package codex

import (
	"context"
	"strings"
)

// DeliveredIDs lists the Roar message IDs whose header starts a user message
// in the thread's recent turns, newest first. Roar writes the header at the
// start of every delivery, so a header quoted inside other text is ignored.
func DeliveredIDs(ctx context.Context, home, thread string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, DeliverTimeout)
	defer cancel()
	cl, err := dial(ctx, Socket(home))
	if err != nil {
		return nil, err
	}
	defer cl.close()
	var r struct {
		Data []struct {
			Items []struct {
				Type    string `json:"type"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := cl.call(ctx, "thread/turns/list", map[string]any{"threadId": thread, "limit": 20}, &r); err != nil {
		return nil, err
	}
	var ids []string
	for _, turn := range r.Data {
		for _, it := range turn.Items {
			if it.Type != "userMessage" || len(it.Content) == 0 {
				continue
			}
			rest, ok := strings.CutPrefix(it.Content[0].Text, "[roar ")
			if id, _, _ := strings.Cut(rest, " "); ok && strings.HasPrefix(id, "rm-") {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

package rex

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
)

// maxPaste keeps each paste event well under Rex's 64 KiB request limit.
const maxPaste = 32 << 10

// Paste sends text as bracketed paste events, so newlines stay in the
// composer instead of submitting. Long text goes in several events.
func (c Client) Paste(ctx context.Context, session, block, text string) error {
	for len(text) > 0 {
		n := min(len(text), maxPaste)
		for n < len(text) && n > 0 && (text[n]&0xc0) == 0x80 {
			n-- // do not split a UTF-8 sequence
		}
		req := map[string]any{
			"session_id": session,
			"block_id":   block,
			"event":      map[string]any{"type": "paste", "payload": map[string]string{"text": text[:n]}},
		}
		in, _ := json.Marshal(req)
		if _, err := c.Run(ctx, "api", "call", "block.event", string(in)); err != nil {
			return err
		}
		text = text[n:]
	}
	return nil
}

// SendKey presses one key, in Rex's key syntax (for example "enter").
func (c Client) SendKey(ctx context.Context, session, block, key string) error {
	_, err := c.Run(ctx, "send-key", "-s", session, "-b", block, key)
	return err
}

// CursorVisible reads DEC mode 25 from the terminal's modes.
func (c Client) CursorVisible(ctx context.Context, session, block string) (bool, error) {
	var out struct {
		Modes []struct {
			Name string `json:"name"`
			Set  bool   `json:"set"`
		} `json:"modes"`
	}
	if err := c.Call(ctx, &out, session, block, "modes", nil); err != nil {
		return false, err
	}
	for _, m := range out.Modes {
		if m.Name == "cursor_visible" {
			return m.Set, nil
		}
	}
	return false, nil
}

// Rows is the terminal's current height. It changes when a client attaches.
func (c Client) Rows(ctx context.Context, session, block string) (int, error) {
	var out struct {
		Rows int `json:"rows"`
	}
	return out.Rows, c.Call(ctx, &out, session, block, "size", nil)
}

// ScreenVT is the screen as VT text with the cursor appended as a final CUP.
func (c Client) ScreenVT(ctx context.Context, session, block string) (string, error) {
	var out struct {
		Content string `json:"content"`
	}
	err := c.Call(ctx, &out, session, block, "format", map[string]any{"format": "vt", "extra_cursor": true})
	return out.Content, err
}

// BlockEvent is one terminal event, such as program_status_changed.
type BlockEvent struct {
	Session string
	Block   string
	Name    string
}

// Events streams block events from every session until ctx ends or the
// stream breaks. The channel closes when the stream ends.
func (c Client) Events(ctx context.Context) (<-chan BlockEvent, error) {
	bin := c.Bin
	if bin == "" {
		bin = "rex"
	}
	args := []string{"events", "--all", "--json"}
	if c.Server != "" {
		args = append([]string{"-S", c.Server}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan BlockEvent, 64)
	go func() {
		defer close(ch)
		defer cmd.Wait()
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			var ev struct {
				Payload struct {
					SessionID string `json:"session_id"`
					Event     struct {
						Type    string `json:"type"`
						Payload struct {
							BlockID string `json:"block_id"`
							Name    string `json:"name"`
						} `json:"payload"`
					} `json:"event"`
				} `json:"payload"`
			}
			if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Payload.Event.Type != "block_event" {
				continue
			}
			select {
			case ch <- BlockEvent{ev.Payload.SessionID, ev.Payload.Event.Payload.BlockID, ev.Payload.Event.Payload.Name}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

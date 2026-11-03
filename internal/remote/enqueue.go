package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vizkid/roar/internal/envelope"
)

// Result is what the remote `roar enqueue` reported.
type Result struct {
	ID     string
	State  string // a store state, or "duplicate"
	Reason string
}

// Exit codes of `roar enqueue` beyond 0 and 2.
const ExitDigest = 6

// Payload is what crosses hosts: the envelope and its attachments' contents.
type Payload struct {
	Envelope envelope.Envelope `json:"envelope"`
	Files    map[string][]byte `json:"files,omitempty"`
}

// StatusRow is one line of a remote `roar status --porcelain`.
type StatusRow struct {
	ID, State, Detail, Proof, Boundary string
}

// Setup runs `roar setup` on the host and returns what it printed.
func (h Host) Setup(ctx context.Context) (string, error) {
	code, screen, err := h.run(ctx, h.roarCommand("setup"), nil, false)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		if l = strings.TrimRight(l, " "); l != "" {
			lines = append(lines, l)
		}
	}
	out := strings.Join(lines, "\n")
	if code != 0 {
		return out, fmt.Errorf("%w: setup exited %d", ErrRefused, code)
	}
	return out, nil
}

func lastError(screen string, code int) string {
	lines := strings.Split(strings.TrimSpace(screen), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "roar: ") {
			return strings.TrimPrefix(l, "roar: ")
		}
	}
	return fmt.Sprintf("exit %d", code)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

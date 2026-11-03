package remote

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vizkid/roar/internal/envelope"
)

// The sender side of the old cross-host path: run `roar enqueue` and `roar
// status --porcelain` in a block on the host. Senders now use Peer, but
// older installed senders still call `roar enqueue`, so the tests keep this
// to drive it.

// Enqueue stores e on the host and runs one delivery pass there. A retry of
// the same envelope reports "duplicate".
func (h Host) Enqueue(ctx context.Context, e envelope.Envelope) (Result, error) {
	return h.EnqueueWithFiles(ctx, e, nil)
}

// EnqueueWithFiles is Enqueue with attachment contents.
func (h Host) EnqueueWithFiles(ctx context.Context, e envelope.Envelope, files map[string][]byte) (Result, error) {
	payload, err := json.Marshal(Payload{Envelope: e, Files: files})
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(payload)
	code, screen, err := h.run(ctx, h.roarCommand("enqueue", "--sha256", hex.EncodeToString(sum[:])), payload, true)
	if err != nil {
		return Result{}, err
	}
	switch code {
	case 0:
		for _, f := range markedLines(screen, "ROAR-RESULT") {
			if len(f) >= 2 && f[0] == e.ID {
				r := Result{ID: f[0], State: f[1]}
				if len(f) > 2 && f[2] != "-" {
					r.Reason = f[2]
				}
				return r, nil
			}
		}
		return Result{}, fmt.Errorf("%w: no result line", ErrUnknown)
	case 126, 127:
		return Result{}, fmt.Errorf("%w: install it with `roar install <host>`", ErrNoPing)
	case ExitDigest:
		return Result{}, fmt.Errorf("%w: the envelope arrived damaged", ErrUnknown)
	}
	return Result{}, fmt.Errorf("%w: %s", ErrRefused, lastError(screen, code))
}

// Status asks the host for the state of messages it holds.
func (h Host) Status(ctx context.Context, ids ...string) ([]StatusRow, error) {
	code, screen, err := h.run(ctx, h.roarCommand(append([]string{"status", "--porcelain"}, ids...)...), nil, false)
	if err != nil {
		return nil, err
	}
	if code == 126 || code == 127 {
		return nil, ErrNoPing
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: %s", ErrRefused, lastError(screen, code))
	}
	var rows []StatusRow
	for _, f := range markedLines(screen, "ROAR-STATUS") {
		for len(f) < 5 {
			f = append(f, "-")
		}
		r := StatusRow{ID: f[0], State: f[1], Detail: f[2], Proof: f[3], Boundary: f[4]}
		for _, p := range []*string{&r.Detail, &r.Proof, &r.Boundary} {
			if *p == "-" {
				*p = ""
			}
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// markedLines returns the fields of the screen lines that start with marker.
func markedLines(screen, marker string) [][]string {
	var out [][]string
	sc := bufio.NewScanner(strings.NewReader(screen))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 1 && f[0] == marker {
			out = append(out, f[1:])
		}
	}
	return out
}

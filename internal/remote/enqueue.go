package remote

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"fmt"
	"runtime"
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

// StatusRow is one line of a remote `roar status --porcelain`.
type StatusRow struct {
	ID, State, Detail, Proof, Boundary string
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

// Install copies this binary to DefaultBin on the host. The host must have
// the same operating system and architecture.
func (h Host) Install(ctx context.Context, binary []byte) error {
	code, screen, err := h.run(ctx, []string{"/usr/bin/uname", "-sm"}, nil, false)
	if err != nil {
		return err
	}
	want := map[string]string{"darwin": "Darwin", "linux": "Linux"}[runtime.GOOS] + " " +
		map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
	if code != 0 || !strings.Contains(screen, want) {
		return fmt.Errorf("%w: host is %q, this binary is %q", ErrRefused, strings.TrimSpace(screen), want)
	}
	script := `stty -echo; set -e; d="$HOME/.local/bin"; mkdir -p "$d"; base64 -d > "$d/.roar.tmp"; ` +
		`chmod 755 "$d/.roar.tmp"; mv "$d/.roar.tmp" "$d/roar"; echo ROAR-INSTALLED`
	code, screen, err = h.run(ctx, []string{"/bin/sh", "-c", script}, binary, false)
	if err != nil {
		return err
	}
	if code != 0 || !strings.Contains(screen, "ROAR-INSTALLED") {
		return fmt.Errorf("%w: install exited %d: %s", ErrRefused, code, lastError(screen, code))
	}
	return nil
}

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

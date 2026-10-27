package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/remote"
)

// cmdEnqueue is the receiving half of a cross-host send. Another host runs
// it here in a detached Rex block and streams the envelope through the
// terminal: raw mode, base64, then an end marker.
func cmdEnqueue(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("enqueue", flag.ContinueOnError)
	digest := fs.String("sha256", "", "digest of the envelope JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *digest == "" {
		return usageErr("roar enqueue --sha256 <digest>")
	}
	if f, ok := env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		// Canonical terminal input limits line length; raw mode does not.
		old, err := term.MakeRaw(int(f.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(f.Fd()), old)
	}
	nl := func(format string, a ...any) { fmt.Fprintf(env.Stdout, format+"\r\n", a...) }
	nl("ROAR-READY")

	// The JSON payload base64-encodes file contents, and the stream
	// base64-encodes the payload again.
	limit := base64.StdEncoding.EncodedLen(base64.StdEncoding.EncodedLen(envelope.MaxFiles) + envelope.MaxText*6 + 64<<10)
	payload, err := readUntilEnd(env.Stdin, limit)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != *digest {
		nl("roar: digest_mismatch")
		return silentExit(remote.ExitDigest)
	}
	var p remote.Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("bad_envelope: %v", err)
	}
	e := p.Envelope
	e.Forwarded = true
	w, err := env.Rex.Whoami(ctx)
	if err != nil {
		return err
	}
	if e.To.HostID != w.HostID {
		return fmt.Errorf("wrong_host: the message is for host %s, this is %s", e.To.HostID, w.HostID)
	}
	st, err := openStore(env)
	if err != nil {
		return err
	}
	st.Prune(env.Now())
	dup, err := st.PutWithFiles(e, env.Now(), p.Files)
	if err != nil {
		return err
	}
	if dup {
		nl("ROAR-RESULT %s duplicate -", e.ID)
		return nil
	}
	if _, err := deliverer(env, st).Block(ctx, e.To.Block); err != nil {
		fmt.Fprintf(env.Stderr, "roar: delivery pass: %v\r\n", err)
	}
	m, err := st.Get(e.ID)
	if err != nil {
		return err
	}
	reason := m.Record.HoldReason
	if reason == "" {
		reason = m.Record.Reason
	}
	if reason == "" {
		reason = "-"
	}
	nl("ROAR-RESULT %s %s %s", e.ID, m.Record.State, reason)
	return nil
}

func readUntilEnd(r io.Reader, limit int) ([]byte, error) {
	var buf bytes.Buffer
	b := make([]byte, 32<<10)
	for {
		n, err := r.Read(b)
		buf.Write(b[:n])
		if i := bytes.Index(buf.Bytes(), []byte("#END#")); i >= 0 {
			data := bytes.Map(func(r rune) rune {
				if r == '\r' || r == '\n' {
					return -1
				}
				return r
			}, buf.Bytes()[:i])
			return base64.StdEncoding.DecodeString(string(data))
		}
		if buf.Len() > limit {
			return nil, errors.New("too_large: envelope exceeds the limit")
		}
		if err != nil {
			return nil, fmt.Errorf("input ended before the end marker: %v", err)
		}
	}
}

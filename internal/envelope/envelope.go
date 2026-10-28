// Package envelope defines the message that Roar stores and delivers.
package envelope

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind says whether the sender expects action.
type Kind string

const (
	Inform  Kind = "inform"
	Request Kind = "request"
)

// MaxText is the largest message body Roar accepts.
const MaxText = 256 << 10

// Address names one Rex pane. IDs resolve only on the server named by HostID.
type Address struct {
	HostID   string `json:"host_id"`
	Endpoint string `json:"endpoint,omitempty"`
	Session  string `json:"session"`
	Block    string `json:"block"`
	// Label is the session label when the message was sent. Display only.
	Label string `json:"label,omitempty"`
}

// Envelope is written once and never changed.
type Envelope struct {
	ID             string    `json:"id"`
	From           Address   `json:"from"`
	To             Address   `json:"to"`
	Kind           Kind      `json:"kind"`
	Text           string    `json:"text"`
	ReplyTo        string    `json:"reply_to,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Forwarded      bool      `json:"forwarded,omitempty"`
	Files          []File    `json:"files,omitempty"`
}

// NormalizeKind maps unknown kinds to Inform.
func NormalizeKind(k Kind) Kind {
	if k == Request {
		return Request
	}
	return Inform
}

// Validate checks the fields every stored envelope must have.
func (e Envelope) Validate() error {
	if !ValidID(e.ID) {
		return fmt.Errorf("invalid message id %q", e.ID)
	}
	if e.To.HostID == "" || e.To.Block == "" {
		return errors.New("recipient host and block are required")
	}
	if e.From.HostID == "" || e.From.Block == "" {
		return errors.New("sender host and block are required")
	}
	if len(e.Text) > MaxText {
		return fmt.Errorf("message is %d bytes; the limit is %d", len(e.Text), MaxText)
	}
	if e.ReplyTo != "" && !ValidID(e.ReplyTo) {
		return fmt.Errorf("invalid reply id %q", e.ReplyTo)
	}
	if e.Kind != Inform && e.Kind != Request {
		return fmt.Errorf("invalid kind %q", e.Kind)
	}
	// Sender fields reach the header and the attachment preamble. A
	// forwarded envelope is the remote host's claim, so refuse controls.
	for _, f := range []string{e.From.HostID, e.From.Session, e.From.Block, e.From.Label,
		e.To.HostID, e.To.Session, e.To.Block, e.To.Label} {
		if hasControl(f) {
			return fmt.Errorf("control character in address %q", f)
		}
	}
	return e.validateFiles()
}

// Header is the single line an agent sees before the body. It carries no
// usage hint: the roar skill teaches the agent to answer.
func (e Envelope) Header() string {
	from := Sanitize(e.From.Label)
	if from == "" {
		from = "?"
	}
	id := Sanitize(e.ID)
	parts := []string{"[roar", id, "from", from + "/" + shortBlock(e.From.Block), string(NormalizeKind(e.Kind))}
	if e.ReplyTo != "" {
		parts = append(parts, "↩"+Sanitize(e.ReplyTo))
	}
	return strings.Join(parts, " ") + "]"
}

// Render is the full text an agent receives.
func (e Envelope) Render() string {
	return e.Header() + "\n" + e.Text
}

// Sanitize removes control characters and collapses whitespace so a
// sender-controlled field cannot forge another header line.
func Sanitize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if isControl(r) || r == ' ' {
			space = b.Len() > 0
			continue
		}
		if r == '[' || r == ']' {
			r = '_'
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func shortBlock(id string) string {
	id = strings.TrimPrefix(id, "block:")
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	return Sanitize(id)
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0)
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, isControl) >= 0
}

// TerminalSafe prepares text for typing into a terminal. It keeps newlines
// and tabs and shows every other control character as a visible symbol, so
// a body cannot end a bracketed paste (ESC [201~) or send keys. A carriage
// return becomes a newline.
func TerminalSafe(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case r < 0x20:
			return 0x2400 + r // the Unicode control picture, as ␛ for ESC
		case r == 0x7f:
			return '␡'
		case r >= 0x80 && r < 0xa0:
			return '�'
		}
		return r
	}, s)
}

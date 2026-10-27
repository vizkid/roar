// Package envelope defines the message that Ping stores and delivers.
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

// MaxText is the largest message body Ping accepts.
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
	return e.validateFiles()
}

// Header is the single line an agent sees before the body.
func (e Envelope) Header() string {
	from := Sanitize(e.From.Label)
	if from == "" {
		from = "?"
	}
	parts := []string{"[ping", Sanitize(e.ID), "from", from + "/" + shortBlock(e.From.Block), string(NormalizeKind(e.Kind))}
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
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' {
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
		return id[len(id)-8:]
	}
	return Sanitize(id)
}

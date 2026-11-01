// Package rexctl speaks Rex's control protocol: one WebSocket that carries
// method calls, events, and calls to client actions. The rex CLI opens a
// new connection per command and refuses requests over 64 KiB; this client
// keeps one open and sends up to the server's limit. The protocol reference
// is each server's GET /llms.txt, section "The Control Connection".
package rexctl

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	protocol    = "rex.control.v1"
	apiRevision = 7
)

var (
	ErrClosed    = errors.New("rex control connection closed")
	ErrNoClient  = errors.New("no such rex client")
	ErrAmbiguous = errors.New("several rex clients have that label")
	ErrTooLarge  = errors.New("rex control message too large")
)

// Info is what a client says about itself, for showing to people.
type Info struct {
	Kind     string `json:"kind,omitempty"` // app, cli, agent, or other
	Platform string `json:"platform,omitempty"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
	Label    string `json:"label,omitempty"`
}

// Error is a failed call, with Rex's error code such as not_found,
// bad_request, refused, not_supported, timeout or client_unavailable.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("rex %s: %s", e.Code, e.Message) }

// Code reports err's Rex error code, or "" when it has none.
func Code(err error) string {
	var re *Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// Event is one server event: its name and its fields.
type Event struct {
	Seq     int64
	Type    string
	Payload json.RawMessage
}

// Principal is who the server says a client is. Clients cannot fake it.
type Principal struct {
	Kind      string   `json:"kind"` // peer, windows_peer, tailscale, or anonymous
	Transport string   `json:"transport"`
	Display   string   `json:"display"`
	UID       *int     `json:"uid,omitempty"`
	UserID    string   `json:"user_id,omitempty"`
	Login     string   `json:"login_name,omitempty"`
	Node      string   `json:"node,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

// Requester is the client that sent a client.perform.
type Requester struct {
	ClientID  string    `json:"client_id"`
	Info      Info      `json:"info"`
	Principal Principal `json:"principal"`
}

// Perform is a request to run one of this client's actions.
type Perform struct {
	CallID    string          `json:"call_id"`
	Action    string          `json:"action"`
	Args      json.RawMessage `json:"args,omitempty"`
	Origin    string          `json:"origin,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	BlockID   string          `json:"block_id,omitempty"`
	Requester Requester       `json:"requester"`
}

// Action is one action this client offers.
type Action struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Args        any    `json:"args,omitempty"` // a JSON Schema
}

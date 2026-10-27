package claude

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vizkid/ping/internal/envelope"
)

// Event kinds read from Claude's transcript. These are undocumented provider
// formats; anything unrecognised proves nothing.
const (
	KindAccepted        = "accepted"          // queue-operation enqueue
	KindRemoved         = "removed"           // queue-operation remove
	KindReceivedTurn    = "received_turn"     // a channel user record: a new turn
	KindReceivedMidTurn = "received_mid_turn" // a queued_command attachment inside a turn
)

// Event is one channel record about one message.
type Event struct {
	ID   string
	Kind string
	At   time.Time
}

// ParseEvent reads one transcript line.
func ParseEvent(line []byte) (Event, bool) {
	var rec struct {
		Type, Operation, Timestamp string
		SessionID                  string `json:"sessionId"`
		Origin                     struct{ Kind, Server string }
		IsMeta                     bool `json:"isMeta"`
		Content                    string
		Message                    struct{ Content json.RawMessage }
		Attachment                 struct {
			Type, Prompt string
			Origin       struct{ Kind, Server string }
			IsMeta       bool `json:"isMeta"`
		}
	}
	if json.Unmarshal(line, &rec) != nil {
		return Event{}, false
	}
	var kind, text string
	switch rec.Type {
	case "queue-operation":
		switch rec.Operation {
		case "enqueue":
			kind = KindAccepted
		case "remove":
			kind = KindRemoved
		default:
			return Event{}, false
		}
		text = rec.Content
	case "user":
		if !rec.IsMeta || rec.Origin.Kind != "channel" || rec.Origin.Server != Server || rec.SessionID == "" ||
			json.Unmarshal(rec.Message.Content, &text) != nil {
			return Event{}, false
		}
		kind = KindReceivedTurn
	case "attachment":
		a := rec.Attachment
		if a.Type != "queued_command" || !a.IsMeta || a.Origin.Kind != "channel" || a.Origin.Server != Server || rec.SessionID == "" {
			return Event{}, false
		}
		kind, text = KindReceivedMidTurn, a.Prompt
	default:
		return Event{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	id := envelopeID(text)
	if err != nil || id == "" {
		return Event{}, false
	}
	return Event{ID: id, Kind: kind, At: at}, true
}

// envelopeID reads the message ID from the opening <channel> tag. A tag with
// a repeated attribute is rejected: Claude does not filter a meta key named
// "source", so a repeat could spoof the source.
func envelopeID(text string) string {
	if !strings.HasPrefix(text, "<channel ") {
		return ""
	}
	tok, err := xml.NewDecoder(strings.NewReader(text)).Token()
	if err != nil {
		return ""
	}
	el, ok := tok.(xml.StartElement)
	if !ok || el.Name.Local != "channel" {
		return ""
	}
	seen := map[string]bool{}
	var source, id string
	for _, a := range el.Attr {
		if seen[a.Name.Local] {
			return ""
		}
		seen[a.Name.Local] = true
		switch a.Name.Local {
		case "source":
			source = a.Value
		case "id":
			id = a.Value
		}
	}
	if source != Server || !envelope.ValidID(id) {
		return ""
	}
	return id
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// TranscriptPath finds the transcript a Claude process is writing now.
// Claude's sessions/<pid>.json follows /clear; the MCP child's own
// CLAUDE_CODE_SESSION_ID does not.
func TranscriptPath(configDir string, claudePID int) (string, error) {
	var s struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
	}
	if err := readJSONFile(filepath.Join(configDir, "sessions", fmt.Sprintf("%d.json", claudePID)), &s); err != nil {
		return "", err
	}
	if s.SessionID == "" || s.Cwd == "" || strings.ContainsAny(s.SessionID, "/\\") {
		return "", fmt.Errorf("sessions file for pid %d lacks a session or cwd", claudePID)
	}
	return filepath.Join(configDir, "projects", nonAlnum.ReplaceAllString(s.Cwd, "-"), s.SessionID+".jsonl"), nil
}

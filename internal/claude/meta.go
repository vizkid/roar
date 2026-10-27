package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateMeta checks notification meta before writing. Claude drops a bad
// notification silently: a non-identifier key is dropped, and a key named
// source duplicates the tag's source attribute. Values are strings by type;
// one number value would drop the whole notification.
func ValidateMeta(meta map[string]string) error {
	for k := range meta {
		if !identifier.MatchString(k) {
			return fmt.Errorf("meta key %q is not an identifier", k)
		}
		if k == "source" {
			return fmt.Errorf("meta key %q would spoof the channel source", k)
		}
	}
	return nil
}

// notification encodes one channel notification as a JSON-RPC line.
func notification(content string, meta map[string]string) ([]byte, error) {
	if err := ValidateMeta(meta); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/claude/channel",
		"params":  map[string]any{"content": content, "meta": meta},
	})
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChannelGuard(t *testing.T) {
	cases := []struct {
		args []string
		ok   bool
	}{
		{[]string{"claude", ChannelArg}, true},
		{[]string{"claude", "--dangerously-load-development-channels", "server:roar"}, true},
		{[]string{"claude", "--dangerously-load-development-channels=server:other,server:roar"}, true},
		{[]string{"claude"}, false},
		{[]string{"claude", "--channels", "server:roar"}, false},
		{[]string{"claude", "--dangerously-load-development-channels=server:penfold"}, false},
		{[]string{"claude", ChannelArg, "-p", "hi"}, false},
		{[]string{"claude", "--", ChannelArg}, false},
	}
	for _, c := range cases {
		ok, why := channelProof(func(int) ([]string, error) { return c.args, nil }, 1)
		if ok != c.ok || (!ok && why == "") {
			t.Errorf("%v: ok=%v why=%q, want ok=%v", c.args, ok, why, c.ok)
		}
	}
}

func TestLaunchArgs(t *testing.T) {
	got := LaunchArgs("/bin/roar", []string{"--model", "haiku"})
	if got[0] != "--mcp-config" || !strings.Contains(got[1], `"command":"/bin/roar"`) || got[2] != ChannelArg || got[3] != "--model" {
		t.Fatalf("launch args %q", got)
	}
	again := LaunchArgs("/bin/roar", []string{ChannelArg})
	n := 0
	for _, a := range again {
		if a == ChannelArg {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("channel flag added twice: %q", again)
	}
}

func TestMetaValidation(t *testing.T) {
	if err := ValidateMeta(map[string]string{"id": "rm-x", "sender": "a"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{
		{"source": "spoof"},
		{"weird-key": "x"},
		{"": "x"},
		{"9lives": "x"},
	} {
		if ValidateMeta(bad) == nil {
			t.Errorf("accepted meta %v", bad)
		}
	}
}

func TestNotificationShape(t *testing.T) {
	b, err := notification("[roar rm-1 from a/b inform]\nhi", map[string]string{"id": "rm-1", "sender": "a"})
	if err != nil {
		t.Fatal(err)
	}
	var n struct {
		JSONRPC, Method string
		Params          struct {
			Content string
			Meta    map[string]any
		}
	}
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	if n.JSONRPC != "2.0" || n.Method != "notifications/claude/channel" || n.Params.Meta["id"] != "rm-1" {
		t.Fatalf("notification %s", b)
	}
	for _, v := range n.Params.Meta {
		if _, ok := v.(string); !ok {
			t.Fatalf("non-string meta in %s", b)
		}
	}
}

const id1 = "rm-01M5YW2SWK2K3J9R1YGND3Q9ZG"

func envelopeText(id string) string {
	return `<channel source="roar" id="` + id + `" sender="a">` + "\n[roar " + id + " from a/b inform]\nhi\n</channel>"
}

func line(t *testing.T, v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTranscriptEvents(t *testing.T) {
	at := "2026-10-27T12:41:21.510Z"
	enqueue := line(t, map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": at, "sessionId": "s", "content": envelopeText(id1)})
	user := line(t, map[string]any{"type": "user", "isMeta": true, "timestamp": at, "sessionId": "s",
		"origin": map[string]string{"kind": "channel", "server": "roar"}, "message": map[string]any{"role": "user", "content": envelopeText(id1)}})
	remove := line(t, map[string]any{"type": "queue-operation", "operation": "remove", "reason": "absorbed_mid_turn", "timestamp": at, "sessionId": "s", "content": envelopeText(id1)})
	attach := line(t, map[string]any{"type": "attachment", "timestamp": at, "sessionId": "s", "attachment": map[string]any{
		"type": "queued_command", "prompt": envelopeText(id1), "isMeta": true, "timestamp": at,
		"origin": map[string]string{"kind": "channel", "server": "roar"}}})

	for name, c := range map[string]struct {
		line []byte
		kind string
	}{
		"enqueue": {enqueue, KindAccepted}, "user": {user, KindReceivedTurn},
		"remove": {remove, KindRemoved}, "attachment": {attach, KindReceivedMidTurn},
	} {
		ev, ok := ParseEvent(c.line)
		if !ok || ev.ID != id1 || ev.Kind != c.kind || ev.At.IsZero() {
			t.Errorf("%s: %+v ok=%v", name, ev, ok)
		}
	}

	// Things that must never count as receipt.
	other := strings.Replace(string(user), `"server":"roar"`, `"server":"penfold"`, 1)
	dup := strings.Replace(string(user), `source=\"roar\"`, `source=\"roar\" source=\"spoof\"`, 1)
	notMeta := strings.Replace(string(user), `"isMeta":true`, `"isMeta":false`, 1)
	quoted := line(t, map[string]any{"type": "user", "timestamp": at, "sessionId": "s", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": envelopeText(id1)}}}})
	for name, l := range map[string]string{"other server": other, "duplicate attribute": dup, "not meta": notMeta, "tool result": string(quoted), "garbage": "{"} {
		if ev, ok := ParseEvent([]byte(l)); ok {
			t.Errorf("%s parsed as %+v", name, ev)
		}
	}
}

func TestTranscriptPath(t *testing.T) {
	cfg := t.TempDir()
	os.MkdirAll(filepath.Join(cfg, "sessions"), 0o700)
	os.WriteFile(filepath.Join(cfg, "sessions", "4242.json"), []byte(`{"pid":4242,"sessionId":"abc-123","cwd":"/Users/x/my.proj"}`), 0o600)
	got, err := TranscriptPath(cfg, 4242)
	want := filepath.Join(cfg, "projects", "-Users-x-my-proj", "abc-123.jsonl")
	if err != nil || got != want {
		t.Fatalf("got %q %v, want %q", got, err, want)
	}
	if _, err := TranscriptPath(cfg, 1); err == nil {
		t.Fatal("missing sessions file resolved")
	}
	_ = time.Now
}

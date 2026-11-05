package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/rexkit/rex"
)

type unreadableInput struct{}

func (unreadableInput) Read([]byte) (int, error) { panic("dry-run read stdin") }

func TestDryRunDoesNotStoreOrReadBody(t *testing.T) {
	h := newHarness(t)
	target := h.pane("preview")
	var out, errs bytes.Buffer
	code := Main([]string{"send", "--dry-run", "--json", "--file", "/missing/attachment", target.label}, Env{
		Stdin: unreadableInput{}, Stdout: &out, Stderr: &errs,
		Getenv: func(k string) string {
			if k == "ROAR_HOME" {
				return h.home
			}
			return ""
		},
		Now: time.Now, Rex: h.rex,
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	// The stable IDs identify exactly the same recipient as a real send.
	var raw map[string]string
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["host_id"] == "" || raw["session"] != target.session || raw["block"] != target.block || raw["window"] == "" {
		t.Fatalf("destination %s", &out)
	}
	entries, err := os.ReadDir(h.home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run changed store: %v %v", entries, err)
	}
}

func TestDryRunReplyUsesOriginalSender(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.pane("alice"), h.pane("bob")
	id := h.send(alice, "send", bob.label, "hello")
	out := h.must(bob, "send", "--dry-run", "--reply", id, "--json")
	if !strings.Contains(out, alice.block) {
		t.Fatalf("reply preview %s", out)
	}
	if got := h.must(alice, "inbox"); got != "" {
		t.Fatalf("dry-run delivered: %s", got)
	}
}

func TestTabAndBlockLabelsStaySeparate(t *testing.T) {
	b := rex.Block{ID: "block:123456", Label: "comm", WindowID: "window:1"}
	for _, label := range []string{"Window d2b81a72", "Review", ""} {
		if got := tabName(b, []rex.Window{{ID: b.WindowID, Label: label}}); got != label {
			t.Fatalf("tab %q became %q", label, got)
		}
	}
}

func TestDryRunMissingTarget(t *testing.T) {
	var out bytes.Buffer
	code := Main([]string{"send", "--dry-run"}, Env{Stdout: &out, Stderr: &out, Stdin: unreadableInput{}, Getenv: func(string) string { return "" }, Now: time.Now})
	if code != 2 || !strings.Contains(out.String(), "<target>") {
		t.Fatalf("%d %s", code, &out)
	}
}

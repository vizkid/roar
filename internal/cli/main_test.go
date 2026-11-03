package cli

import (
	"flag"
	"strings"
	"testing"
)

// roard ran under the two-minute command deadline, so the service manager
// restarted it every 120 s and each restart left a lost client behind.
func TestServiceCommandsHaveNoDeadline(t *testing.T) {
	for name, want := range map[string]bool{"daemon": false, "send": true, "status": true} {
		ctx, cancel := commandContext(name)
		_, has := ctx.Deadline()
		cancel()
		if has != want {
			t.Errorf("%s: deadline %v, want %v", name, has, want)
		}
		if ctx.Err() == nil {
			t.Errorf("%s: cancel did not end the context", name)
		}
	}
}

// flag.Parse consumes "--" itself, so the old loop parsed what followed it
// as flags: "target -- body --request" turned request mode on (review,
// 2026-11-03).
func TestParseKeepsEverythingAfterDashDash(t *testing.T) {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	req := fs.Bool("request", false, "")
	pos, err := parse(fs, []string{"target", "--", "body", "--request", "-x"})
	if err != nil || *req || strings.Join(pos, "|") != "target|body|--request|-x" {
		t.Fatalf("pos %q request %v err %v", pos, *req, err)
	}
	pos, err = parse(fs, []string{"--request", "target", "words"})
	if err != nil || !*req || strings.Join(pos, "|") != "target|words" {
		t.Fatalf("pos %q request %v err %v", pos, *req, err)
	}
}

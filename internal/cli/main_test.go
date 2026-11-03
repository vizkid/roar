package cli

import (
	"flag"
	"io"
	"strings"
	"testing"
	"time"
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

// Review, 2026-11-03: install read none of its settings. It ran under the
// two-minute command deadline with fixed waits, so the slow profile's ten
// minutes never applied.
func TestInstallUsesItsSettings(t *testing.T) {
	ctx, cancel := commandContext("install")
	defer cancel()
	if _, has := ctx.Deadline(); has {
		t.Fatal("install runs under the command deadline")
	}
	env := Env{Getenv: func(k string) string {
		if k == "ROAR_SET" {
			return "install.deadline=7m,install.ready_check=1s,install.ready_timeout=3s"
		}
		return ""
	}, Stderr: io.Discard}
	h, s, err := installHost(env, "Baroni")
	if err != nil {
		t.Fatal(err)
	}
	if h.Timeout != 7*time.Minute || h.Poll != time.Second || h.ReadyTimeout != 3*time.Second || s.Duration("install.deadline") != 7*time.Minute {
		t.Fatalf("host %+v", h)
	}
}

// Review, 2026-11-03: store limits and retention were constants, so
// changing them needed a rebuild.
func TestStorePolicyFromSettings(t *testing.T) {
	home := t.TempDir()
	env := Env{Getenv: func(k string) string {
		switch k {
		case "ROAR_HOME":
			return home
		case "ROAR_SET":
			return "store.unread_messages=3,store.retention=1h,store.unread_per_host=1MiB"
		}
		return ""
	}, Stderr: io.Discard}
	if err := applyTimings(env, "send"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { applyTimings(Env{Getenv: func(string) string { return "" }, Stderr: io.Discard}, "send") })
	st, err := openStore(env)
	if err != nil {
		t.Fatal(err)
	}
	if st.Limits.Messages != 3 || st.Limits.PerHost != 1<<20 || st.Retention != time.Hour {
		t.Fatalf("limits %+v retention %v", st.Limits, st.Retention)
	}
}

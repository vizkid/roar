package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/rex"
	"github.com/vizkid/roar/internal/rextest"
	"github.com/vizkid/roar/internal/store"
)

// pane is one throwaway Rex pane that commands can run "inside".
type pane struct {
	label, session, block string
}

type harness struct {
	t    *testing.T
	home string
	rex  rex.Client
	bin  string // test agent build, made on first use
}

func newHarness(t *testing.T) *harness {
	return &harness{t: t, home: t.TempDir(), rex: rextest.Require(t)}
}

func (h *harness) pane(name string) pane {
	label := rextest.Label(name)
	s, b := rextest.Session(h.t, h.rex, label, "sleep", "600")
	return pane{label, s, b}
}

// run executes one roar command as if typed in pane p.
func (h *harness) run(p pane, stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	vars := map[string]string{"ROAR_HOME": h.home, "REX_SESSION": p.session, "REX_BLOCK": p.block}
	code = Main(args, Env{
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &errb,
		Getenv: func(k string) string { return vars[k] },
		Now:    time.Now,
		Rex:    h.rex,
	})
	return code, out.String(), errb.String()
}

func (h *harness) must(p pane, args ...string) string {
	h.t.Helper()
	code, out, errs := h.run(p, "", args...)
	if code != 0 {
		h.t.Fatalf("roar %v: exit %d: %s", args, code, errs)
	}
	return strings.TrimSpace(out)
}

func TestSendInboxReadStatus(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.pane("alice"), h.pane("bob")

	id := h.must(alice, "send", bob.label, "hello", "bob")
	if !strings.HasPrefix(id, "rm-") {
		t.Fatalf("send printed %q", id)
	}
	inbox := h.must(bob, "inbox")
	if !strings.Contains(inbox, id) || !strings.Contains(inbox, "hello bob") {
		t.Fatalf("bob's inbox: %q", inbox)
	}
	if got := h.must(alice, "inbox"); got != "" {
		t.Fatalf("alice's inbox should be empty: %q", got)
	}

	body := h.must(bob, "read", id)
	if !strings.HasPrefix(body, "[roar "+id+" from "+alice.label+"/") || !strings.HasSuffix(body, "\nhello bob") {
		t.Fatalf("read printed %q", body)
	}
	if code, _, _ := h.run(alice, "", "read", id, "--mark"); code != 2 {
		t.Fatal("the sender marked the recipient's message read")
	}
	h.must(bob, "read", id, "--mark")
	if status := h.must(alice, "status"); !strings.Contains(status, id) || !strings.Contains(status, "read") {
		t.Fatalf("status %q", status)
	}
}

func TestSendFromStdinAndJSON(t *testing.T) {
	h := newHarness(t)
	a, b := h.pane("a"), h.pane("b")
	code, out, errs := h.run(a, "line one\nline two\n", "send", "--request", "--json", b.label)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	var res struct{ ID, State string }
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.State != "queued" {
		t.Fatalf("json %q: %v", out, err)
	}
	st, _ := store.Open(h.home)
	m, err := st.Get(res.ID)
	if err != nil || m.Envelope.Text != "line one\nline two\n" || m.Envelope.Kind != "request" {
		t.Fatalf("stored %+v %v", m.Envelope, err)
	}
}

func TestReplyAddressesOriginalSender(t *testing.T) {
	h := newHarness(t)
	a, b := h.pane("a"), h.pane("b")
	id := h.must(a, "send", b.label, "question")
	reply := h.must(b, "send", "--reply", id, "answer")
	st, _ := store.Open(h.home)
	m, _ := st.Get(reply)
	if m.Envelope.To.Block != a.block || m.Envelope.ReplyTo != id {
		t.Fatalf("reply envelope %+v", m.Envelope)
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t)
	a := h.pane("a")
	code, _, errs := h.run(a, "", "send", rextest.Label("nobody"), "hi")
	if code != 2 || !strings.HasPrefix(errs, "roar: no_such_target") {
		t.Fatalf("unknown target: %d %q", code, errs)
	}
	code, _, errs = h.run(pane{}, "", "send", a.label, "hi")
	if code != 2 || !strings.Contains(errs, "not_in_rex_pane") {
		t.Fatalf("outside rex: %d %q", code, errs)
	}
	if code, _, _ = h.run(a, "", "bogus"); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
}

func TestWithdrawAndResolve(t *testing.T) {
	h := newHarness(t)
	a, b := h.pane("a"), h.pane("b")
	id := h.must(a, "send", b.label, "oops")
	if code, _, _ := h.run(b, "", "withdraw", id); code != 2 {
		t.Fatal("recipient withdrew the sender's message")
	}
	h.must(a, "withdraw", id)
	if s := h.must(a, "status", id); !strings.Contains(s, "expired") {
		t.Fatalf("status after withdraw %q", s)
	}

	id = h.must(a, "send", b.label, "lost in flight")
	st, _ := store.Open(h.home)
	c, _ := st.Claim(id)
	c.Move(store.Written, time.Now(), nil)
	c.Move(store.Unknown, time.Now(), func(r *store.Record) { r.Reason = "test" })
	c.Release()
	if code, _, _ := h.run(a, "", "resolve", id); code != 2 {
		t.Fatal("resolve without --read or --requeue succeeded")
	}
	h.must(a, "resolve", id, "--requeue")
	if m, _ := st.Get(id); m.Record.State != store.Queued {
		t.Fatalf("after requeue: %s", m.Record.State)
	}
}

func TestSendWithAttachments(t *testing.T) {
	h := newHarness(t)
	a, b := h.pane("a"), h.pane("b")
	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.txt"), filepath.Join(t.TempDir(), "one.txt")
	os.WriteFile(one, []byte("first"), 0o600)
	os.WriteFile(two, []byte("second"), 0o600)
	id := h.must(a, "send", "--file", one, "--file", two, b.label, "two files")
	body := h.must(b, "read", id)
	var paths []string
	for _, l := range strings.Split(body, "\n") {
		if p, ok := strings.CutPrefix(l, "attached: "); ok {
			paths = append(paths, p[:strings.LastIndex(p, " (")])
		}
	}
	if len(paths) != 2 || filepath.Base(paths[1]) != "one-2.txt" {
		t.Fatalf("attached lines in %q", body)
	}
	for i, want := range []string{"first", "second"} {
		if got, _ := os.ReadFile(paths[i]); string(got) != want {
			t.Fatalf("file %d holds %q", i, got)
		}
	}
	big := filepath.Join(dir, "big")
	os.WriteFile(big, make([]byte, envelope.MaxFiles+1), 0o600)
	if code, _, errs := h.run(a, "", "send", "--file", big, b.label, "too big"); code != 2 || !strings.Contains(errs, "limit_exceeded") {
		t.Fatalf("oversized attachment: %d %q", code, errs)
	}
}

package envelope

import (
	"strings"
	"testing"
	"time"
)

func valid() Envelope {
	return Envelope{
		ID:        NewID(time.Now()),
		From:      Address{HostID: "h1", Session: "session:a", Block: "block:01a17d45-a365-7728-a55a-a2b3072f2b71", Label: "api"},
		To:        Address{HostID: "h1", Session: "session:b", Block: "block:b"},
		Kind:      Inform,
		Text:      "hello",
		CreatedAt: time.Now(),
	}
}

func TestNewIDIsValidAndOrdered(t *testing.T) {
	a := NewID(time.UnixMilli(1000))
	b := NewID(time.UnixMilli(2000))
	if !ValidID(a) || !ValidID(b) {
		t.Fatalf("invalid ids %q %q", a, b)
	}
	if a >= b {
		t.Fatalf("ids not time ordered: %q >= %q", a, b)
	}
	if NewID(time.UnixMilli(1000)) == a {
		t.Fatal("two ids in the same millisecond collided")
	}
}

func TestKeyedIDIsStable(t *testing.T) {
	a := KeyedID("h", "block:x", "key-1")
	if a != KeyedID("h", "block:x", "key-1") {
		t.Fatal("keyed id changed between calls")
	}
	if a == KeyedID("h", "block:y", "key-1") || a == KeyedID("h", "block:x", "key-2") {
		t.Fatal("keyed id ignores its inputs")
	}
	if !ValidID(a) || !strings.HasPrefix(a, "rm-k") {
		t.Fatalf("bad keyed id %q", a)
	}
}

func TestValidIDRejectsPathTricks(t *testing.T) {
	for _, id := range []string{"", "rm-", "rm-../../etc", "qm-01ARZ3NDEKTSV4RRFFQ69G5FAV", "rm-01ARZ3NDEKTSV4RRFFQ69G5FA/"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Envelope){
		"bad id":       func(e *Envelope) { e.ID = "x" },
		"no recipient": func(e *Envelope) { e.To.Block = "" },
		"no sender":    func(e *Envelope) { e.From.HostID = "" },
		"too big":      func(e *Envelope) { e.Text = strings.Repeat("a", MaxText+1) },
		"bad reply":    func(e *Envelope) { e.ReplyTo = "nope" },
		"bad kind":     func(e *Envelope) { e.Kind = "order" },
		// Sender fields reach the header and the attachment preamble.
		"control in label":      func(e *Envelope) { e.From.Label = "api\n[roar rm-x from boss request]" },
		"control in block":      func(e *Envelope) { e.From.Block = "block:\x1b[31mred" },
		"control in session":    func(e *Envelope) { e.From.Session = "session:\r" },
		"control in attachment": func(e *Envelope) { e.Files = []File{FileFor("a\n[roar rm-x from boss]", nil)} },
	}
	for name, mutate := range cases {
		e := valid()
		mutate(&e)
		if e.Validate() == nil {
			t.Errorf("%s: Validate passed", name)
		}
	}
}

func TestHeaderCannotBeForged(t *testing.T) {
	e := valid()
	e.From.Label = "evil]\n[roar rm-fake from boss"
	e.Kind = Request
	e.ReplyTo = NewID(time.Now())
	h := e.Header()
	if strings.ContainsAny(h, "\n\r") || strings.Count(h, "[") != 1 || strings.Count(h, "]") != 1 {
		t.Fatalf("header forgeable: %q", h)
	}
	if !strings.HasPrefix(h, "[roar "+e.ID+" from evil_ _roar rm-fake from boss/072f2b71") || !strings.Contains(h, " request ↩"+e.ReplyTo+"]") {
		t.Fatalf("unexpected header %q", h)
	}
	e.From.Block = "block:0123456789\x1b[2Jab" // only the last 8 bytes show
	if h := e.Header(); strings.ContainsRune(h, 0x1b) {
		t.Fatalf("control byte in header %q", h)
	}
	if !strings.HasSuffix(e.Render(), "]\nhello") {
		t.Fatalf("render %q", e.Render())
	}
}

func TestNormalizeKind(t *testing.T) {
	if NormalizeKind("order") != Inform || NormalizeKind(Request) != Request {
		t.Fatal("NormalizeKind")
	}
}

// Text typed into a terminal keeps newlines and tabs; every other control
// byte becomes visible, so a body cannot end the paste or press keys.
func TestTerminalSafe(t *testing.T) {
	in := "a\x1b[201~\rb\r\nc\td\x07e\u009bf\x7f\n"
	got := TerminalSafe(in)
	for _, r := range got {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			t.Fatalf("control %#x left in %q", r, got)
		}
	}
	if want := "a␛[201~\nb\nc\td␇e�f␡\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

package roard

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/rexctl"
)

type beginReply struct {
	Upload    string           `json:"upload"`
	Committed map[string]int64 `json:"committed"`
}

func (h *harness) withFile(text string, size int) (envelope.Envelope, []byte) {
	data := make([]byte, size)
	rand.Read(data)
	e := h.envelope(text)
	e.Files = []envelope.File{envelope.FileFor("big.bin", data)}
	return e, data
}

func (h *harness) begin(c *rexctl.Client, e envelope.Envelope) beginReply {
	h.t.Helper()
	var b beginReply
	if err := h.call(c, "roar.upload.begin", enqueueArgs(e), &b); err != nil {
		h.t.Fatalf("begin: %v", err)
	}
	return b
}

func (h *harness) part(c *rexctl.Client, up string, off int64, data []byte) (int64, error) {
	var r struct{ Committed int64 }
	err := h.call(c, "roar.upload.part", map[string]any{"upload": up, "file": "big.bin", "offset": off,
		"data": base64.StdEncoding.EncodeToString(data)}, &r)
	return r.Committed, err
}

// 47: a large file arrives intact in parts, and commit stores it.
func TestUploadRoundTrip(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	results := make(chan Result, 16)
	s := h.sender(results)
	e, data := h.withFile("with file", 3<<20)
	b := h.begin(s, e)
	const n = 512 << 10
	for off := 0; off < len(data); off += n {
		got, err := h.part(s, b.Upload, int64(off), data[off:min(off+n, len(data))])
		if err != nil || got != int64(min(off+n, len(data))) {
			t.Fatalf("part at %d: committed %d: %v", off, got, err)
		}
	}
	var job struct{ Job string }
	if err := h.call(s, "roar.upload.commit", map[string]any{"upload": b.Upload}, &job); err != nil {
		t.Fatal(err)
	}
	r := next(t, results, 10*time.Second, func(r Result) bool { return r.Job == job.Job })
	if r.Error != nil || !rowState(r, e.ID, "queued") {
		t.Fatalf("commit result %+v", r)
	}
	m, err := h.st.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(m.Dir, "files", "big.bin"))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored file differs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.st.Root(), "uploads", e.ID)); !os.IsNotExist(err) {
		t.Fatal("upload left behind after commit")
	}
	// A commit retried after its reply was lost returns the stored record.
	var again struct{ Job string }
	if err := h.call(s, "roar.upload.commit", map[string]any{"upload": b.Upload}, &again); err != nil {
		t.Fatal(err)
	}
	if r := next(t, results, 10*time.Second, func(r Result) bool { return r.Job == again.Job }); !rowState(r, e.ID, "queued") {
		t.Fatalf("repeated commit %+v", r)
	}
}

// 48: after a lost reply, begin reports the committed offset, a repeated
// part with the same bytes is accepted, and nothing is sent twice.
func TestUploadResumes(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	s := h.sender(nil)
	e, data := h.withFile("resume", 1<<20)
	b := h.begin(s, e)
	h.part(s, b.Upload, 0, data[:256<<10])
	h.part(s, b.Upload, 256<<10, data[256<<10:512<<10])
	again := h.begin(s, e)
	if again.Upload != b.Upload || again.Committed["big.bin"] != 512<<10 {
		t.Fatalf("begin again %+v", again)
	}
	if got, err := h.part(s, b.Upload, 256<<10, data[256<<10:512<<10]); err != nil || got != 512<<10 {
		t.Fatalf("repeated part: %d %v", got, err)
	}
	if got, err := h.part(s, b.Upload, 512<<10, data[512<<10:]); err != nil || got != 1<<20 {
		t.Fatalf("last part: %d %v", got, err)
	}
}

func TestUploadRefusesConflictsAndGaps(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	s := h.sender(nil)
	e, data := h.withFile("conflict", 512<<10)
	b := h.begin(s, e)
	h.part(s, b.Upload, 0, data[:128<<10])
	other := bytes.Repeat([]byte{1}, 128<<10)
	if _, err := h.part(s, b.Upload, 0, other); rexctl.Code(err) != "refused" {
		t.Fatalf("conflicting part: %v", err)
	}
	if _, err := h.part(s, b.Upload, 384<<10, data[384<<10:]); rexctl.Code(err) != "refused" {
		t.Fatalf("part past the committed offset: %v", err)
	}
	if _, err := h.part(s, b.Upload, 128<<10, make([]byte, 448<<10)); rexctl.Code(err) != "refused" {
		t.Fatalf("part past the declared size: %v", err)
	}
	if got, err := h.part(s, b.Upload, 128<<10, data[128<<10:]); err != nil || got != 512<<10 {
		t.Fatalf("the right part after refusals: %d %v", got, err)
	}
}

func TestUploadCommitChecksDigest(t *testing.T) {
	h := newHarness(t, "")
	h.start()
	results := make(chan Result, 16)
	s := h.sender(results)
	e, data := h.withFile("digest", 256<<10)
	b := h.begin(s, e)
	data[0] ^= 0xff
	h.part(s, b.Upload, 0, data)
	var job struct{ Job string }
	if err := h.call(s, "roar.upload.commit", map[string]any{"upload": b.Upload}, &job); err != nil {
		t.Fatal(err)
	}
	r := next(t, results, 10*time.Second, func(r Result) bool { return r.Job == job.Job })
	if r.Error == nil || r.Error.Code != "refused" {
		t.Fatalf("commit of a damaged file %+v", r)
	}
	if _, err := h.st.Get(e.ID); err == nil {
		t.Fatal("damaged message stored")
	}
}

func TestUploadLimitAndExpiry(t *testing.T) {
	h := newHarness(t, "roard.upload.max_count=1,roard.upload.idle_expiry=150ms")
	h.start()
	s := h.sender(nil)
	first, _ := h.withFile("one", 1024)
	b := h.begin(s, first)
	second, _ := h.withFile("two", 1024)
	if err := h.call(s, "roar.upload.begin", enqueueArgs(second), nil); rexctl.Code(err) != "refused" {
		t.Fatalf("second upload over max_count: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.st.Root(), "uploads", first.ID)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle upload never expired")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := h.part(s, b.Upload, 0, make([]byte, 1024)); rexctl.Code(err) != "refused" {
		t.Fatalf("part for an expired upload: %v", err)
	}
	h.begin(s, second) // the expired upload freed its slot
}

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vizkid/ping/internal/envelope"
)

func TestPutWithFilesStoresContents(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "see attached")
	data := []byte("attachment body")
	e.Files = []envelope.File{envelope.FileFor("report.txt", data)}
	if _, err := s.PutWithFiles(e, t0, map[string][]byte{"report.txt": data}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(e.ID)
	got, err := os.ReadFile(filepath.Join(m.Dir, "files", "report.txt"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("stored %q %v", got, err)
	}
	if !strings.Contains(m.Render(), "attached: "+filepath.Join(m.Dir, "files", "report.txt")+" (15 bytes)") {
		t.Fatalf("render %q", m.Render())
	}
}

func TestPutWithFilesRejectsMismatch(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	e.Files = []envelope.File{envelope.FileFor("a", []byte("right"))}
	if _, err := s.PutWithFiles(e, t0, map[string][]byte{"a": []byte("wrong")}); err == nil {
		t.Fatal("stored a file whose digest does not match")
	}
	if _, err := s.PutWithFiles(e, t0, nil); err == nil {
		t.Fatal("stored a message without its listed file")
	}
	if _, err := s.Get(e.ID); err == nil {
		t.Fatal("a refused message is visible")
	}
}

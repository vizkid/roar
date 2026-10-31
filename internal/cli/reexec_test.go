package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// A watcher runs for days. It must notice that `roar install` replaced
// its binary, by rename or by rewriting the file in place.
func TestBinaryReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roar")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("old")
	info, _ := os.Stat(path)
	b := binary{path, info}
	if b.replaced() {
		t.Fatal("unchanged binary reported replaced")
	}
	os.Remove(path)
	if b.replaced() {
		t.Fatal("a missing binary counts as replaced")
	}
	write("new build")
	if !b.replaced() {
		t.Fatal("rewritten binary not seen")
	}
	tmp := path + ".tmp"
	os.WriteFile(tmp, []byte("old"), 0o755)
	os.Chtimes(tmp, info.ModTime(), info.ModTime())
	os.Rename(tmp, path)
	if !b.replaced() {
		t.Fatal("renamed-in binary of the same size and time not seen")
	}
}

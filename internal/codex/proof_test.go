package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Only a header that starts a user message counts: Roar writes it there,
// and a body quoting a header does not.
func TestDeliveredIDs(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "idle", "")
	d.inputs = []string{
		"[roar rm-01OLD from a/1 inform]\nfirst",
		"plain text from the operator",
		"see [roar rm-01QUOTED from a/1 inform]",
		"[roar rm-01NEW from a/1 request]\nsecond",
	}
	ids, err := DeliveredIDs(context.Background(), d.home, "T1")
	if err != nil || !slices.Equal(ids, []string{"rm-01NEW", "rm-01OLD"}) {
		t.Fatalf("ids %v, %v", ids, err)
	}
	if _, err := DeliveredIDs(context.Background(), "/nonexistent", "T1"); err == nil {
		t.Fatal("no daemon should be an error")
	}
}

// Codex links its socket into a private directory, and Roar follows that
// link. It refuses a socket in a directory others can write, where another
// user could have put it.
func TestDialChecksTheSocketDirectory(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("T1", "idle", "")
	home, err := os.MkdirTemp("/tmp", "rc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	os.MkdirAll(home+"/app-server-control", 0o700)
	os.Symlink(Socket(d.home), Socket(home))
	os.Chmod(filepath.Dir(Socket(d.home)), 0o700)
	if _, err := DeliveredIDs(context.Background(), home, "T1"); err != nil {
		t.Fatalf("a linked socket in a private directory: %v", err)
	}
	os.Chmod(filepath.Dir(Socket(d.home)), 0o777)
	if _, err := DeliveredIDs(context.Background(), home, "T1"); err == nil {
		t.Fatal("dialled a socket in a directory anyone can write")
	}
}

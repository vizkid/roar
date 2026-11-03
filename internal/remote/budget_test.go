package remote

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// Review, 2026-11-03: the whole-send budget was checked only after a try
// failed, so one stalled dial ran its own full timeout past it. Here the
// server takes the connection and never answers.
func TestSendBudgetBoundsADial(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	p := &Peer{Endpoint: "unix://" + sock, Label: "roard",
		Settings: settings(t, "sender.transfer.base=100ms,sender.connect.timeout=5s")}
	start := time.Now()
	_, err = p.Send(t.Context(), peerEnvelope("budget"), nil)
	if took := time.Since(start); err == nil || took > 2*time.Second {
		t.Fatalf("send took %v (budget 100ms, dial timeout 5s): %v", took, err)
	}
}

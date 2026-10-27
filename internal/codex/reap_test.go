package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReapStopsServerOfDeadOwner(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ping-codex-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// A stand-in server whose command line names the socket, in its own group.
	srv := exec.Command("/bin/sh", "-c", "sleep 60; true", "unix://"+dir+"/server.sock") // "; true" stops sh from exec-ing sleep
	srv.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { srv.Wait(); close(exited) }()
	defer syscall.Kill(-srv.Process.Pid, syscall.SIGKILL)

	// An owner that is already dead.
	dead := exec.Command("/usr/bin/true")
	dead.Run()
	os.WriteFile(filepath.Join(dir, ownerFile), fmt.Appendf(nil, "%d %d\n", dead.Process.Pid, srv.Process.Pid), 0o600)

	if n := ReapOrphans(); n < 1 {
		t.Fatalf("reaped %d", n)
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("orphaned server still running")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("directory left behind")
	}
}

func TestReapLeavesLiveOwnersAlone(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "ping-codex-")
	defer os.RemoveAll(dir)
	writeOwner(dir, 999999)
	ReapOrphans()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("reaped a live owner's directory")
	}
}

package store

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests in this file run real child processes, because file locks and crash
// recovery only mean something across processes.

const helperEnv = "PING_STORE_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(helper(mode, os.Getenv("PING_HOME"), os.Getenv("PING_ID")))
	}
	os.Exit(m.Run())
}

// helper is the child process body. Exit codes: 0 won, 1 lost, 2 error.
func helper(mode, root, id string) int {
	s, err := Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	switch mode {
	case "race":
		c, err := s.Claim(id)
		if err != nil {
			return 1
		}
		m := c.Message()
		if m.Record.State != Queued {
			return 1
		}
		if err := c.Move(Written, time.Now(), nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		time.Sleep(200 * time.Millisecond)
		c.Release()
		return 0
	case "crash-submitting":
		c, err := s.Claim(id)
		if err != nil {
			return 2
		}
		if err := c.Move(Submitting, time.Now(), nil); err != nil {
			return 2
		}
		os.Exit(0) // die holding the claim; the kernel drops the lock
	case "adapter":
		l, err := s.HoldAdapter(id)
		if err != nil {
			return 1
		}
		fmt.Println("holding")
		time.Sleep(time.Hour)
		l.Release()
	}
	return 2
}

func child(t *testing.T, root, mode, id string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode, "PING_HOME="+root, "PING_ID="+id)
	return cmd
}

func TestRacingProcessesClaimOnce(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	put(t, s, e)
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := child(t, s.Root(), "race", e.ID).Run()
			if ee, ok := err.(*exec.ExitError); ok {
				codes[i] = ee.ExitCode()
			} else if err != nil {
				codes[i] = 2
			}
		}()
	}
	wg.Wait()
	won := 0
	for _, c := range codes {
		if c == 2 {
			t.Fatalf("child error, codes %v", codes)
		}
		if c == 0 {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d processes delivered, want 1 (codes %v)", won, codes)
	}
}

func TestCrashedSubmitterSettlesToUnknown(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	put(t, s, e)
	if out, err := child(t, s.Root(), "crash-submitting", e.ID).CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	n, err := s.SettleOrphans(t0)
	if err != nil || n != 1 {
		t.Fatalf("settled %d, err %v", n, err)
	}
	m, _ := s.Get(e.ID)
	if m.Record.State != Unknown || m.Record.Reason != "interrupted_while_typing" {
		t.Fatalf("record %+v", m.Record)
	}
}

func TestSettleSkipsLiveSubmitter(t *testing.T) {
	s := newStore(t)
	e := msg("block:r", "x")
	put(t, s, e)
	c, err := s.Claim(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	if err := c.Move(Submitting, t0, nil); err != nil {
		t.Fatal(err)
	}
	other, _ := Open(s.Root())
	if n, err := other.SettleOrphans(t0); err != nil || n != 0 {
		t.Fatalf("settled a live claim: n=%d err=%v", n, err)
	}
}

func TestAdapterLockFollowsProcessLife(t *testing.T) {
	s := newStore(t)
	const block = "block:agent"
	if s.AdapterLive(block) {
		t.Fatal("live before any adapter")
	}
	cmd := child(t, s.Root(), "adapter", block)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	nr, _ := out.Read(buf)
	if !strings.Contains(string(buf[:nr]), "holding") {
		t.Fatalf("adapter child said %q", buf[:nr])
	}
	if !s.AdapterLive(block) {
		t.Fatal("not live while the adapter holds the lock")
	}
	if _, err := s.HoldAdapter(block); err == nil {
		t.Fatal("second adapter got the lock")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if s.AdapterLive(block) {
		t.Fatal("still live after the adapter died")
	}
}

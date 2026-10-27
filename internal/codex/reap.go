package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// The App Server runs in its own process group, so a pane hangup does not
// reach it. `ping codex` stops it on SIGHUP and SIGTERM, but nothing can
// clean up after SIGKILL. Each launch therefore records its owner, and the
// next launch reaps servers whose owner died.

const ownerFile = "owner"

func writeOwner(dir string, serverPGID int) error {
	return os.WriteFile(filepath.Join(dir, ownerFile), fmt.Appendf(nil, "%d %d\n", os.Getpid(), serverPGID), 0o600)
}

// ReapOrphans stops App Servers left by dead `ping codex` processes and
// removes their directories. It returns how many it reaped.
func ReapOrphans() int {
	dirs, _ := filepath.Glob("/tmp/ping-codex-*")
	n := 0
	for _, dir := range dirs {
		var owner, pgid int
		b, err := os.ReadFile(filepath.Join(dir, ownerFile))
		if err != nil {
			continue // still starting, or not ours
		}
		if _, err := fmt.Sscan(string(b), &owner, &pgid); err != nil || owner <= 0 || pgid <= 0 {
			continue
		}
		if alive(owner) {
			continue
		}
		// The group leader may be gone and its ID reused: only signal a
		// process whose command names this directory.
		if servesDir(pgid, dir) {
			syscall.Kill(-pgid, syscall.SIGTERM)
		}
		os.RemoveAll(dir)
		n++
	}
	return n
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func servesDir(pid int, dir string) bool {
	out, err := exec.Command("/bin/ps", "-o", "command=", "-p", fmt.Sprint(pid)).Output()
	return err == nil && strings.Contains(string(out), dir+"/server.sock")
}

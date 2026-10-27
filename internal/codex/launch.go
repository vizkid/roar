package codex

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Session is one running App Server and the proxy in front of it.
type Session struct {
	Dir       string
	ProxySock string
	Proxy     *Proxy
	server    *exec.Cmd
	http      *http.Server
}

// Start runs `codex app-server` on a Unix socket in a private directory and
// serves the proxy beside it. The directory sits under /tmp because macOS
// limits socket paths to 104 bytes. Roar never listens on TCP: a loopback
// WebSocket without auth would let any local process drive the agent.
func Start(ctx context.Context, bin string) (*Session, error) {
	ReapOrphans()
	dir, err := os.MkdirTemp("/tmp", "roar-codex-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s := &Session{Dir: dir, ProxySock: filepath.Join(dir, "proxy.sock")}
	serverSock := filepath.Join(dir, "server.sock")
	logFile, err := os.Create(filepath.Join(dir, "app-server.log"))
	if err != nil {
		s.Stop()
		return nil, err
	}
	s.server = exec.Command(bin, "app-server", "--listen", "unix://"+serverSock)
	s.server.Stdout, s.server.Stderr = logFile, logFile
	// Its own process group: Ctrl-C in the pane is for the TUI, not the server.
	s.server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := s.server.Start(); err != nil {
		s.Stop()
		return nil, err
	}
	if err := writeOwner(dir, s.server.Process.Pid); err != nil {
		s.Stop()
		return nil, err
	}
	if err := waitSocket(ctx, serverSock, 20*time.Second); err != nil {
		s.Stop()
		return nil, fmt.Errorf("codex app-server did not start (log: %s): %w", logFile.Name(), err)
	}
	s.Proxy = NewProxy(serverSock)
	ln, err := net.Listen("unix", s.ProxySock)
	if err != nil {
		s.Stop()
		return nil, err
	}
	s.http = &http.Server{Handler: s.Proxy.Handler()}
	go s.http.Serve(ln)
	return s, nil
}

// Stop ends the proxy and the App Server and removes the directory.
func (s *Session) Stop() {
	if s.http != nil {
		s.http.Close()
	}
	if s.server != nil && s.server.Process != nil {
		pgid := s.server.Process.Pid
		syscall.Kill(-pgid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { s.server.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
		}
	}
	os.RemoveAll(s.Dir)
}

func waitSocket(ctx context.Context, path string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("no socket at %s after %s", path, d)
}

// Command testagent imitates an agent's composer for Roar's tests. Built as
// "claude" or "codex", it draws that agent's composer and reports program
// status (OSC 7501) like the real agent, so fallback tests can run in real
// Rex panes without model calls.
//
// Environment:
//
//	TESTAGENT_LOG       append one JSON line per submitted prompt
//	TESTAGENT_CTL       control file, polled: idle, working, done, blocked, clear, exit
//	TESTAGENT_ON_PASTE  state to enter as soon as a paste arrives (simulates a race)
//	TESTAGENT_WORK_MS   how long a submitted turn stays working (default 300)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type agent struct {
	mu     sync.Mutex
	marker string
	app    string
	state  string
	draft  []rune
	hidden bool
}

func main() {
	a := &agent{marker: "❯ ", app: "claude-code", state: "idle"}
	if filepath.Base(os.Args[0]) == "codex" {
		a.marker, a.app = "› ", "codex"
	}
	old, err := term.MakeRaw(0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer term.Restore(0, old)
	os.Stdout.WriteString("\x1b[?1049h\x1b[?2004h")
	defer os.Stdout.WriteString("\x1b[?2004l\x1b[?1049l")
	a.redraw()
	go a.control()
	a.read()
}

func (a *agent) setState(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = s
	a.hidden = s == "blocked"
	a.redrawLocked()
}

func (a *agent) redraw() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.redrawLocked()
}

func (a *agent) redrawLocked() {
	_, rows, err := term.GetSize(1)
	if err != nil || rows < 5 {
		rows = 24
	}
	rule := strings.Repeat("─", 40)
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	fmt.Fprintf(&b, "\x1b[%d;1H%s", rows-2, rule)
	fmt.Fprintf(&b, "\x1b[%d;1H%s", rows-1, a.marker)
	if len(a.draft) == 0 {
		b.WriteString("\x1b[2mTry something\x1b[0m")
	} else {
		b.WriteString(strings.ReplaceAll(string(a.draft), "\n", "\r\n  "))
	}
	fmt.Fprintf(&b, "\x1b[%d;1H%s", rows, rule)
	fmt.Fprintf(&b, "\x1b]7501;state=%s:app=%s\x1b\\", a.state, a.app)
	if a.hidden {
		b.WriteString("\x1b[?25l")
	} else {
		lines := strings.Split(string(a.draft), "\n")
		last := []rune(lines[len(lines)-1])
		fmt.Fprintf(&b, "\x1b[?25h\x1b[%d;%dH", rows-1+len(lines)-1, 3+len(last))
	}
	os.Stdout.WriteString(b.String())
}

func (a *agent) read() {
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		pending = a.consume(append(pending, buf[:n]...))
	}
}

// consume handles complete input and returns any unfinished paste.
func (a *agent) consume(in []byte) []byte {
	const start, end = "\x1b[200~", "\x1b[201~"
	s := string(in)
	for len(s) > 0 {
		if strings.HasPrefix(s, start) {
			i := strings.Index(s, end)
			if i < 0 {
				return []byte(s)
			}
			a.insert(s[len(start):i])
			if p := os.Getenv("TESTAGENT_ON_PASTE"); p != "" {
				a.setState(p)
			}
			s = s[i+len(end):]
			continue
		}
		switch c := s[0]; {
		case c == '\r':
			a.submit()
		case c == 0x1b:
			s = s[1:] // ignore other escapes
			continue
		case c >= 0x20:
			r := []rune(s)[0]
			a.insert(string(r))
			s = s[len(string(r)):]
			continue
		}
		s = s[1:]
	}
	return nil
}

func (a *agent) insert(t string) {
	a.mu.Lock()
	a.draft = append(a.draft, []rune(t)...)
	a.mu.Unlock()
	a.redraw()
}

func (a *agent) submit() {
	a.mu.Lock()
	text := string(a.draft)
	a.draft = nil
	a.mu.Unlock()
	if text == "" {
		return
	}
	if path := os.Getenv("TESTAGENT_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			json.NewEncoder(f).Encode(map[string]string{"text": text})
			f.Close()
		}
	}
	a.setState("working")
	ms, _ := strconv.Atoi(os.Getenv("TESTAGENT_WORK_MS"))
	if ms <= 0 {
		ms = 300
	}
	time.AfterFunc(time.Duration(ms)*time.Millisecond, func() { a.setState("done") })
}

func (a *agent) control() {
	path := os.Getenv("TESTAGENT_CTL")
	if path == "" {
		return
	}
	for range time.Tick(50 * time.Millisecond) {
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			continue
		}
		os.WriteFile(path, nil, 0o600)
		switch cmd := strings.TrimSpace(string(b)); cmd {
		case "exit":
			os.Stdout.WriteString("\x1b[?2004l\x1b[?1049l")
			os.Exit(0)
		case "clear":
			a.mu.Lock()
			a.draft = nil
			a.mu.Unlock()
			a.redraw()
		default:
			a.setState(cmd)
		}
	}
}

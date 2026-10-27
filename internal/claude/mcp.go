package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// Config is everything the adapter needs. Tests replace the functions.
type Config struct {
	Store     *store.Store
	Session   string
	Block     string
	ParentPID int
	// ParentArgs reads a process's argv (the guard reads Claude's).
	ParentArgs func(pid int) ([]string, error)
	// Transcript returns the transcript Claude writes now; it changes on /clear.
	Transcript func() (string, error)
	// AgentStatus returns Claude's program status from Rex (idle, working, ...).
	AgentStatus func(ctx context.Context) (string, error)
	Now         func() time.Time
	// Writer identifies this adapter process in each message it writes.
	Writer string
	Poll   time.Duration
	Grace  time.Duration
	Log    io.Writer
}

// instructions stay short: the roar skill teaches the agent to answer.
const instructions = `Messages from other agents arrive as <channel source="roar"> blocks. Use the roar skill to answer them.`

// Serve speaks MCP over in/out until in closes or ctx ends. It declares the
// channel capability only when Claude was started with the channel flag and
// no other adapter serves this pane.
func Serve(ctx context.Context, cfg Config, in io.Reader, out io.Writer) error {
	if cfg.Poll == 0 {
		cfg.Poll = 250 * time.Millisecond
	}
	if cfg.Grace == 0 {
		cfg.Grace = 2 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	w := &writer{out: out}
	var lock *store.AdapterLock
	defer func() {
		if lock != nil {
			lock.Release()
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			ok, why := channelProof(cfg.ParentArgs, cfg.ParentPID)
			if ok {
				l, err := cfg.Store.HoldAdapter(cfg.Block)
				if err != nil {
					ok, why = false, "adapter_busy: another adapter serves this pane"
				} else {
					lock = l
				}
			}
			if !ok {
				fmt.Fprintf(cfg.Log, "roar mcp: no channel: %s\n", why)
			}
			w.reply(req.ID, initializeResult(ok))
		case "notifications/initialized":
			if lock != nil {
				go newDeliverer(cfg, w).run(ctx)
			}
		case "tools/list":
			w.reply(req.ID, map[string]any{"tools": []any{}})
		case "resources/list":
			w.reply(req.ID, map[string]any{"resources": []any{}})
		case "prompts/list":
			w.reply(req.ID, map[string]any{"prompts": []any{}})
		case "ping": // MCP's keep-alive method
			w.reply(req.ID, map[string]any{})
		default:
			if len(req.ID) > 0 && string(req.ID) != "null" {
				w.fail(req.ID, -32601, "method not found")
			}
		}
	}
	return sc.Err()
}

func initializeResult(channel bool) map[string]any {
	caps := map[string]any{"tools": map[string]any{}}
	if channel {
		caps["experimental"] = map[string]any{"claude/channel": map[string]any{}}
	}
	return map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    caps,
		"serverInfo":      map[string]any{"name": Server, "version": "0.1.0"},
		"instructions":    instructions,
	}
}

// writer serializes everything written to Claude's stdin pipe.
type writer struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *writer) line(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.out.Write(append(b, '\n'))
	return err
}

func (w *writer) reply(id json.RawMessage, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	w.line(b)
}

func (w *writer) fail(id json.RawMessage, code int, msg string) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	w.line(b)
}

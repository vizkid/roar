package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The fake Antigravity agent API. The agent, built as agy with
// TESTAGENT_API set to a directory, accepts conversation C1 with token T1
// and takes each message from that directory's inbox. `agy agentapi
// send-message` plays the client: its ANTIGRAVITY_LS_ADDRESS is the
// directory. A file named garble there makes it deliver, then print a
// reply that is not JSON.

const apiConversation, apiToken = "C1", "T1"

// agentapi runs the client and exits.
func agentapi(args []string) {
	if len(args) != 3 || args[0] != "send-message" {
		fmt.Println("Usage: agentapi send-message [--title=<title>] <recipient_id> <content>")
		os.Exit(1)
	}
	dir := os.Getenv("ANTIGRAVITY_LS_ADDRESS")
	reply := func(v map[string]any) {
		json.NewEncoder(os.Stdout).Encode(v)
		os.Exit(0)
	}
	switch {
	case dir == "":
		reply(map[string]any{"error": "ANTIGRAVITY_LS_ADDRESS is not set"})
	case os.Getenv("ANTIGRAVITY_CSRF_TOKEN") != apiToken:
		reply(map[string]any{"response": map[string]any{}, "error": "rpc error: code = Unauthenticated desc = unauthenticated: missing CSRF token"})
	case args[1] != apiConversation:
		reply(map[string]any{"response": map[string]any{}, "error": "rpc error: code = NotFound desc = conversation not found"})
	}
	f, err := os.OpenFile(filepath.Join(dir, "inbox"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		reply(map[string]any{"error": err.Error()})
	}
	json.NewEncoder(f).Encode(map[string]string{"text": args[2]})
	f.Close()
	if _, err := os.Stat(filepath.Join(dir, "garble")); err == nil {
		fmt.Println("panic: runtime error")
		os.Exit(2)
	}
	reply(map[string]any{"response": map[string]any{"sendMessage": map[string]string{"recipientId": args[1], "content": args[2]}}})
}

// serveAPI delivers each inbox message as the real agent API does: it
// starts a turn at rest and joins a running one.
func (a *agent) serveAPI(dir string) {
	inbox := filepath.Join(dir, "inbox")
	seen := 0
	for range time.Tick(50 * time.Millisecond) {
		b, err := os.ReadFile(inbox)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		for ; seen < len(lines); seen++ {
			var m struct{ Text string }
			if json.Unmarshal([]byte(lines[seen]), &m) == nil {
				a.receive(m.Text)
			}
		}
	}
}

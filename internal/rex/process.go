package rex

import (
	"context"
	"path"
)

// Proc is a process Rex reports for a terminal.
type Proc struct {
	PID         int    `json:"pid"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	InvokedPath string `json:"invoked_path"`
	Argv0       string `json:"argv0"`
	Script      string `json:"script"`
	Cwd         string `json:"cwd"`
}

// Exit is how a terminal's program ended.
type Exit struct {
	Code  int    `json:"exit_code"`
	Error string `json:"error"`
}

// Process is the result of the terminal's `process` method.
type Process struct {
	LastExit   *Exit `json:"last_exit"`
	Foreground *Proc `json:"foreground"`
}

func (c Client) Process(ctx context.Context, session, block string) (Process, error) {
	var p Process
	return p, c.Call(ctx, &p, session, block, "process", nil)
}

// Agent names the agent a process runs: "claude", "codex", or "".
//
// Claude reports its version as the process name, so the invoked path
// decides. Codex installed through npm runs under node with a codex script.
func (p Proc) Agent() string {
	for _, s := range []string{p.InvokedPath, p.Argv0} {
		if path.Base(s) == "claude" {
			return "claude"
		}
	}
	for _, s := range []string{p.Script, p.Path, p.InvokedPath, p.Argv0} {
		if path.Base(s) == "codex" {
			return "codex"
		}
	}
	return ""
}

// StatusRecord is one program status report (OSC 7501).
type StatusRecord struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	Kind         string `json:"kind"`
	App          string `json:"app"`
	EffectiveApp string `json:"effective_app"`
	Owner        *struct {
		PID  int    `json:"pid"`
		Name string `json:"name"`
	} `json:"owner"`
}

// ProgramStatus returns the root status record, or nil when none exists.
func (c Client) ProgramStatus(ctx context.Context, session, block string) (*StatusRecord, error) {
	var out struct {
		Records []StatusRecord `json:"records"`
	}
	if err := c.Call(ctx, &out, session, block, "program_status", nil); err != nil {
		return nil, err
	}
	for i := range out.Records {
		if out.Records[i].ID == "" {
			return &out.Records[i], nil
		}
	}
	return nil, nil
}

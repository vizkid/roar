package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchSkillRoot(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		agent string
		vars  map[string]string
		want  string
	}{
		{"claude", map[string]string{"HOME": home}, filepath.Join(home, ".claude")},
		{"claude", map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": "/c"}, "/c"},
		{"codex", map[string]string{"HOME": home}, filepath.Join(home, ".codex")},
		{"codex", map[string]string{"HOME": home, "CODEX_HOME": "/x"}, "/x"},
	}
	for _, c := range cases {
		env := Env{Getenv: func(k string) string { return c.vars[k] }}
		if got := skillRoot(env, c.agent); got != c.want {
			t.Errorf("%s %v: root %q, want %q", c.agent, c.vars, got, c.want)
		}
	}

	// A failed install warns on stderr and lets the launch go on.
	var errb bytes.Buffer
	env := Env{Stderr: &errb, Getenv: func(k string) string {
		return map[string]string{"HOME": home, "CODEX_HOME": "/dev/null/x"}[k]
	}}
	installSkill(env, "codex")
	if !strings.Contains(errb.String(), "skill not installed") {
		t.Fatalf("stderr %q", errb.String())
	}
}

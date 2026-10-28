package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const hookArgs = " hook codex"

// Codex installs the skill in a Codex home and adds the SessionStart hook
// group that binds each pane to its thread.
func Codex(home, roar string) (string, error) {
	if strings.IndexFunc(roar, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("roar's path %q has a control character; it cannot go in a hook command", roar)
	}
	var done []string
	if err := installSkill(home, &done); err != nil {
		return "", err
	}
	path := filepath.Join(home, "hooks.json")
	var file object
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return "", err
	default:
		if err := json.Unmarshal(b, &file); err != nil {
			return "", err
		}
	}
	changed, err := addHook(&file, quote(roar)+hookArgs)
	if err != nil {
		return "", err
	}
	if changed != "" {
		if err := writeFile(path, file); err != nil {
			return "", err
		}
		done = append(done, changed)
	}
	return summary(done), nil
}

// addHook puts Roar's group in SessionStart. It replaces an earlier Roar
// group in place, else appends: Codex keys hook trust by position, so other
// groups keep theirs.
func addHook(file *object, command string) (string, error) {
	var hooks object
	if raw, ok := file.get("hooks"); ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return "", err
		}
	}
	var groups []json.RawMessage
	if raw, ok := hooks.get("SessionStart"); ok {
		if err := json.Unmarshal(raw, &groups); err != nil {
			return "", err
		}
	}
	type entry struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	ours, _ := json.Marshal(map[string][]entry{"hooks": {{"command", command, 5}}})
	changed := "hook added"
	at := len(groups)
	for i, g := range groups {
		var group struct{ Hooks []entry }
		json.Unmarshal(g, &group)
		for _, h := range group.Hooks {
			if h.Command == command {
				return "", nil
			}
			if isRoarHook(h.Command) {
				at, changed = i, "hook updated"
			}
		}
	}
	if at == len(groups) {
		groups = append(groups, ours)
	} else {
		groups[at] = ours
	}
	raw, _ := json.Marshal(groups)
	hooks.set("SessionStart", raw)
	raw, _ = json.Marshal(hooks)
	file.set("hooks", raw)
	return changed, nil
}

func isRoarHook(command string) bool {
	bin, ok := strings.CutSuffix(command, hookArgs)
	return ok && filepath.Base(strings.Trim(bin, "'")) == "roar"
}

// quote makes a path safe as the first word of a shell command. A path
// that needs no quoting stays bare, so an installed hook keeps its text.
func quote(path string) string {
	if strings.ContainsAny(path, " '\"\\$`!*?[]{}()<>|&;#~") {
		return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	}
	return path
}

// writeFile replaces path atomically, keeping its mode.
func writeFile(path string, file object) error {
	b, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hooks.json.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

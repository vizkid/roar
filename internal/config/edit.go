package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The file is edited line by line, not decoded and re-encoded, so every
// other line and comment stays as the operator wrote it.

var header = regexp.MustCompile(`^\s*\[`)

func hostHeader(host string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(host) {
		return "[hosts." + host + "]"
	}
	return "[hosts." + strconv.Quote(host) + "]"
}

// section returns the line range [start, end) that holds the keys of host's
// section, or of the top level when host is empty. found is false when the
// host has no section yet.
func section(lines []string, host string) (start, end int, found bool) {
	if host == "" {
		for i, l := range lines {
			if header.MatchString(l) {
				return 0, i, true
			}
		}
		return 0, len(lines), true
	}
	want := hostHeader(host)
	for i, l := range lines {
		if strings.TrimSpace(strings.SplitN(l, "#", 2)[0]) == want {
			end := len(lines)
			for j := i + 1; j < len(lines); j++ {
				if header.MatchString(lines[j]) {
					end = j
					break
				}
			}
			return i + 1, end, true
		}
	}
	return 0, 0, false
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

func writeLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// put writes `name = value` into host's section. It replaces the line
// that already sets name, under whatever table spelling, keeping that
// line's key text: the same key text means another key in another table.
// Other lines that set name are removed. The edited text must still load.
func put(path, host, name, value string, k *Key) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	if defs := definitions(lines, host, name); len(defs) > 0 {
		_, rest, _ := splitKey(lines[defs[0]])
		key := lines[defs[0]][:len(lines[defs[0]])-len(rest)]
		lines[defs[0]] = key + "= " + value
		lines = removeLines(lines, defs[1:])
		return writeChecked(path, lines, host, name, k)
	}
	line := strconv.Quote(name) + " = " + value
	if name == "profile" {
		line = "profile = " + value
	}
	start, end, found := section(lines, host)
	if !found {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, hostHeader(host), line)
		return writeChecked(path, lines, host, name, k)
	}
	// After the section's last key, before the blank lines that end it.
	at := end
	for at > start && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	lines = append(lines[:at], append([]string{line}, lines[at:]...)...)
	return writeChecked(path, lines, host, name, k)
}

// removeLines drops the lines at the given ascending indexes.
func removeLines(lines []string, at []int) []string {
	for i := len(at) - 1; i >= 0; i-- {
		lines = append(lines[:at[i]], lines[at[i]+1:]...)
	}
	return lines
}

// writeChecked replaces the file only when the edited text still decodes
// and, for a key, its new value parses where the reader will look.
func writeChecked(path string, lines []string, host, name string, k *Key) error {
	f, err := parseFile(path, strings.Join(lines, "\n")+"\n")
	if err != nil {
		return fmt.Errorf("the edit would break the file: %w", err)
	}
	if k != nil {
		m := f.keys
		if host != "" {
			m = f.hosts[host]
		}
		if _, err := parse(*k, m[name]); err != nil {
			return fmt.Errorf("the edit would break the file: %s: %v", name, err)
		}
	}
	return writeLines(path, lines)
}

// tomlValue writes a parsed value in canonical TOML: numbers and booleans
// bare, the rest as strings. The text the user typed is not reused, since
// parsing accepts spellings such as "1" for true that TOML reads otherwise.
func tomlValue(k Key, v any, value string) string {
	switch n := v.(type) {
	case bool:
		return strconv.FormatBool(n)
	case int:
		return strconv.Itoa(n)
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return strconv.Quote(value)
}

// Set writes one key, for host's section when host is set, after checking
// the key and its value.
func Set(path, host, name, value string) error {
	k, ok := byName[name]
	if !ok {
		return fmt.Errorf("unknown key %s", name)
	}
	if host != "" && !k.Host {
		return fmt.Errorf("%s does not apply per host", name)
	}
	v, err := parse(k, value)
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	return put(path, host, name, tomlValue(k, v, value), &k)
}

// SetProfile chooses a profile for the file, or for host.
func SetProfile(path, host, name string) error {
	f, err := readFile(path)
	if err != nil {
		return err
	}
	if _, err := f.profileKeys(name); err != nil {
		return err
	}
	return put(path, host, "profile", strconv.Quote(name), nil)
}

// Unset removes every line that sets name in the file, or in host's
// section, whatever table spelling it uses.
func Unset(path, host, name string) error {
	lines, err := readLines(path)
	if err != nil || lines == nil {
		return err
	}
	defs := definitions(lines, host, name)
	if len(defs) == 0 {
		return nil
	}
	return writeChecked(path, removeLines(lines, defs), host, name, nil)
}

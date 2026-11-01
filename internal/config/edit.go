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

// keyLine matches a line that sets name, quoted or bare.
func keyLine(name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return regexp.MustCompile(`^\s*(?:"` + q + `"|` + q + `)\s*=`)
}

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

// put writes `name = value` into host's section, replacing an earlier line.
func put(path, host, name, value string) error {
	lines, err := readLines(path)
	if err != nil {
		return err
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
		return writeLines(path, lines)
	}
	re := keyLine(name)
	for i := start; i < end; i++ {
		if re.MatchString(lines[i]) {
			lines[i] = line
			return writeLines(path, lines)
		}
	}
	// After the section's last key, before the blank lines that end it.
	at := end
	for at > start && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	lines = append(lines[:at], append([]string{line}, lines[at:]...)...)
	return writeLines(path, lines)
}

// tomlValue writes a checked value: numbers and booleans bare, the rest as
// strings.
func tomlValue(k Key, value string) string {
	switch k.Kind {
	case KindFloat, KindInt, KindBool:
		return value
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
	if _, err := parse(k, value); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	return put(path, host, name, tomlValue(k, value))
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
	return put(path, host, "profile", strconv.Quote(name))
}

// Unset removes one key from the file, or from host's section.
func Unset(path, host, name string) error {
	lines, err := readLines(path)
	if err != nil || lines == nil {
		return err
	}
	start, end, found := section(lines, host)
	if !found {
		return nil
	}
	re := keyLine(name)
	for i := start; i < end; i++ {
		if re.MatchString(lines[i]) {
			return writeLines(path, append(lines[:i], lines[i+1:]...))
		}
	}
	return nil
}

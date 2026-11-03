package config

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A key can be written several ways that mean the same setting: a quoted
// "sender.call.timeout", a bare dotted sender.call.timeout, or timeout
// under a [sender.call] table, also inside [hosts.<label>]. The reader
// flattens all of them, so the editor must find all of them.

var tableLine = regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(#.*)?$`)

// splitKey reads a TOML key at the start of s: bare or quoted segments
// joined by dots. It returns the segments and the rest of s, or false.
func splitKey(s string) (segs []string, rest string, ok bool) {
	for {
		s = strings.TrimLeft(s, " \t")
		var seg string
		switch {
		case strings.HasPrefix(s, `"`):
			end := 1
			for end < len(s) && (s[end] != '"' || s[end-1] == '\\') {
				end++
			}
			if end >= len(s) {
				return nil, "", false
			}
			u, err := strconv.Unquote(s[:end+1])
			if err != nil {
				return nil, "", false
			}
			seg, s = u, s[end+1:]
		case strings.HasPrefix(s, "'"):
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return nil, "", false
			}
			seg, s = s[1:end+1], s[end+2:]
		default:
			n := 0
			for n < len(s) && (s[n] == '_' || s[n] == '-' || s[n] >= '0' && s[n] <= '9' || s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z') {
				n++
			}
			if n == 0 {
				return nil, "", false
			}
			seg, s = s[:n], s[n:]
		}
		segs = append(segs, seg)
		s = strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(s, ".") {
			return segs, s, true
		}
		s = s[1:]
	}
}

// definitions returns the lines that set name: in host's section when host
// is set, else at the top level, under any table spelling.
func definitions(lines []string, host, name string) []int {
	var prefix []string
	if host != "" {
		prefix = []string{"hosts", host}
	}
	var table []string
	var at []int
	for i, l := range lines {
		if m := tableLine.FindStringSubmatch(l); m != nil {
			table, _, _ = splitKey(m[1])
			continue
		}
		segs, rest, ok := splitKey(l)
		if !ok || !strings.HasPrefix(rest, "=") {
			continue
		}
		full := append(slices.Clone(table), segs...)
		if !slices.Equal(full[:min(len(prefix), len(full))], prefix) {
			continue
		}
		key := full[len(prefix):]
		if host == "" && len(key) > 0 && (key[0] == "hosts" || key[0] == "profiles") {
			continue
		}
		if strings.Join(key, ".") == name {
			at = append(at, i)
		}
	}
	return at
}

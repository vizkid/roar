// Package config holds every timer, limit and backoff Roar uses, so an
// operator can tune them without a build. docs/ROARD.md "Configuration"
// describes the sources and their precedence.
package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options says where settings come from. Host is the Rex host label a
// command talks to, or empty; Profile and Set are ROAR_PROFILE and ROAR_SET.
type Options struct {
	Path    string
	Profile string
	Set     string
	Host    string
}

// Settings is every key's effective value and the source it came from.
type Settings struct {
	vals map[string]any
	src  map[string]string
}

// DefaultPath is ROAR_CONFIG, else $XDG_CONFIG_HOME/roar/config.toml, else
// ~/.config/roar/config.toml.
func DefaultPath(getenv func(string) string, home string) string {
	if p := getenv("ROAR_CONFIG"); p != "" {
		return p
	}
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "roar", "config.toml")
	}
	return filepath.Join(home, ".config", "roar", "config.toml")
}

// FromEnv reads the options a command gets from its environment.
func FromEnv(getenv func(string) string, home, host string) Options {
	return Options{Path: DefaultPath(getenv, home), Profile: getenv("ROAR_PROFILE"), Set: getenv("ROAR_SET"), Host: host}
}

// Load reads the settings in precedence order: defaults, the profile, the
// file, the host section, then ROAR_SET. It returns warnings for unknown
// keys and an error, naming the key, for a bad value.
func Load(o Options) (*Settings, []string, error) {
	s := &Settings{vals: map[string]any{}, src: map[string]string{}}
	var warn []string
	for _, k := range keys {
		v, err := parse(k, k.Default)
		if err != nil {
			return nil, nil, fmt.Errorf("built-in default %s: %v", k.Name, err)
		}
		s.vals[k.Name], s.src[k.Name] = v, "default"
	}
	f, err := readFile(o.Path)
	if err != nil {
		return nil, nil, err
	}
	apply := func(layer map[string]any, source string, hostOnly bool) error {
		names := make([]string, 0, len(layer))
		for n := range layer {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			k, ok := byName[name]
			if !ok {
				warn = append(warn, fmt.Sprintf("%s: unknown key %s", source, name))
				continue
			}
			if hostOnly && !k.Host {
				warn = append(warn, fmt.Sprintf("%s: %s does not apply per host", source, name))
				continue
			}
			v, err := parse(k, layer[name])
			if err != nil {
				return fmt.Errorf("%s: %s: %v", source, name, err)
			}
			s.vals[name], s.src[name] = v, source
		}
		return nil
	}
	profile := o.Profile
	if profile == "" {
		profile = f.profile
	}
	if profile != "" && profile != "default" {
		p, err := f.profileKeys(profile)
		if err != nil {
			return nil, nil, err
		}
		if err := apply(p, "profile "+profile, false); err != nil {
			return nil, nil, err
		}
	}
	if err := apply(f.keys, "file", false); err != nil {
		return nil, nil, err
	}
	if host, ok := f.hosts[o.Host]; ok && o.Host != "" {
		section := "host " + o.Host
		if hp, ok := host["profile"].(string); ok {
			p, err := f.profileKeys(hp)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", section, err)
			}
			hostProfile := map[string]any{}
			for n, v := range p {
				if k, ok := byName[n]; ok && k.Host {
					hostProfile[n] = v // a profile's other keys are not per host
				}
			}
			if err := apply(hostProfile, section+" profile "+hp, true); err != nil {
				return nil, nil, err
			}
		}
		rest := map[string]any{}
		for n, v := range host {
			if n != "profile" {
				rest[n] = v
			}
		}
		if err := apply(rest, section, true); err != nil {
			return nil, nil, err
		}
	}
	set, err := parseSet(o.Set)
	if err != nil {
		return nil, nil, err
	}
	if err := apply(set, "ROAR_SET", false); err != nil {
		return nil, nil, err
	}
	return s, warn, nil
}

// parseSet reads ROAR_SET: key=value pairs separated by commas.
func parseSet(s string) (map[string]any, error) {
	m := map[string]any{}
	for _, pair := range strings.Split(s, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("ROAR_SET: %q: want key=value", pair)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

// Source names where a key's value came from, such as "default", "file",
// "profile slow", "host Baroni" or "ROAR_SET".
func (s *Settings) Source(name string) string { return s.src[name] }

func (s *Settings) get(name string) any {
	v, ok := s.vals[name]
	if !ok {
		panic("config: no key " + name)
	}
	return v
}

func (s *Settings) Duration(name string) time.Duration { return s.get(name).(time.Duration) }

// DefaultDuration is a duration key's built-in default, for code that runs
// without loaded settings. It panics on a key that is not a duration.
func DefaultDuration(name string) time.Duration {
	k := byName[name]
	v, err := parse(k, k.Default)
	if err != nil {
		panic("config: " + name + ": " + err.Error())
	}
	return v.(time.Duration)
}
func (s *Settings) Size(name string) int64    { return s.get(name).(int64) }
func (s *Settings) Float(name string) float64 { return s.get(name).(float64) }
func (s *Settings) Int(name string) int       { return s.get(name).(int) }
func (s *Settings) Bool(name string) bool     { return s.get(name).(bool) }

// Value is a key's effective value as Roar writes it.
func (s *Settings) Value(name string) string {
	switch v := s.get(name).(type) {
	case time.Duration:
		return v.String()
	case int64:
		if k := byName[name]; k.Kind == KindRate {
			return formatSize(v) + "/s"
		}
		return formatSize(v)
	default:
		return fmt.Sprint(v)
	}
}

func formatSize(n int64) string {
	for _, u := range units {
		if u.scale > 1 && n >= u.scale && n%u.scale == 0 {
			return fmt.Sprintf("%d%s", n/u.scale, u.suffix)
		}
	}
	return fmt.Sprintf("%dB", n)
}

// Backoff reads the backoff keys under prefix. reset_after is optional.
func (s *Settings) Backoff(prefix string) Backoff {
	b := Backoff{
		Initial: s.Duration(prefix + ".initial"),
		Factor:  s.Float(prefix + ".factor"),
		Max:     s.Duration(prefix + ".max"),
		Jitter:  s.Float(prefix + ".jitter"),
	}
	if _, ok := s.vals[prefix+".reset_after"]; ok {
		b.ResetAfter = s.Duration(prefix + ".reset_after")
	}
	return b
}

// Deadline reads prefix.timeout and prefix.rtt_factor.
func (s *Settings) Deadline(prefix string) Deadline {
	return Deadline{Timeout: s.Duration(prefix + ".timeout"), RTTFactor: s.Float(prefix + ".rtt_factor")}
}

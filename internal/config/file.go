package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// file is the config file, split into its parts. Keys are flattened to
// their dotted names.
type file struct {
	profile  string
	keys     map[string]any
	hosts    map[string]map[string]any
	profiles map[string]map[string]any
}

func readFile(path string) (file, error) {
	f := file{keys: map[string]any{}, hosts: map[string]map[string]any{}, profiles: map[string]map[string]any{}}
	var tree map[string]any
	if _, err := toml.DecodeFile(path, &tree); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f, nil
		}
		return f, fmt.Errorf("%s: %w", path, err)
	}
	for name, v := range tree {
		switch name {
		case "profile":
			p, ok := v.(string)
			if !ok {
				return f, fmt.Errorf("%s: profile: want a name", path)
			}
			f.profile = p
		case "hosts", "profiles":
			sections, ok := v.(map[string]any)
			if !ok {
				return f, fmt.Errorf("%s: %s: want tables", path, name)
			}
			for label, sec := range sections {
				m, ok := sec.(map[string]any)
				if !ok {
					return f, fmt.Errorf("%s: %s.%s: want a table", path, name, label)
				}
				flat := map[string]any{}
				flatten("", m, flat)
				if name == "hosts" {
					f.hosts[label] = flat
				} else {
					f.profiles[label] = flat
				}
			}
		default:
			flatten("", map[string]any{name: v}, f.keys)
		}
	}
	return f, nil
}

// flatten turns nested tables into dotted keys, so [sender.upload]
// part_size and "sender.upload.part_size" mean the same.
func flatten(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		name := k
		if prefix != "" {
			name = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flatten(name, sub, out)
		} else {
			out[name] = v
		}
	}
}

func builtinProfile(name string) (map[string]any, bool) {
	switch name {
	case "", "default":
		return map[string]any{}, true
	case "slow":
		m := map[string]any{}
		for _, k := range keys {
			if k.Slow != "" {
				m[k.Name] = k.Slow
			}
		}
		return m, true
	}
	return nil, false
}

// profile is a built-in profile with the file's section of the same name
// on top, or the file's section alone.
func (f file) profileKeys(name string) (map[string]any, error) {
	base, builtin := builtinProfile(name)
	user, inFile := f.profiles[name]
	if !builtin && !inFile {
		return nil, fmt.Errorf("profile %q: no such profile", name)
	}
	m := map[string]any{}
	for k, v := range base {
		m[k] = v
	}
	for k, v := range user {
		m[k] = v
	}
	return m, nil
}

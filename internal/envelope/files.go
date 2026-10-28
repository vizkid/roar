package envelope

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// MaxFiles is the total size of a message's attachments.
const MaxFiles = 16 << 20

// File describes one attachment. The contents travel beside the envelope.
type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// FileFor describes data stored under name.
func FileFor(name string, data []byte) File {
	sum := sha256.Sum256(data)
	return File{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

// Matches reports whether data is this file's contents.
func (f File) Matches(data []byte) bool {
	return FileFor(f.Name, data) == f
}

// UniqueNames gives each path its base name, suffixing repeats: a.txt,
// a-2.txt, a-3.txt.
func UniqueNames(paths []string) []string {
	seen := map[string]int{}
	out := make([]string, len(paths))
	for i, p := range paths {
		name := filepath.Base(p)
		seen[name]++
		if n := seen[name]; n > 1 {
			ext := filepath.Ext(name)
			name = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), n, ext)
		}
		out[i] = name
	}
	return out
}

func (e Envelope) validateFiles() error {
	var total int64
	seen := map[string]bool{}
	for _, f := range e.Files {
		if f.Name == "" || strings.HasPrefix(f.Name, ".") || strings.ContainsAny(f.Name, "/\\") || hasControl(f.Name) || len(f.Name) > 128 {
			return fmt.Errorf("invalid attachment name %q", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("attachment name %q repeats", f.Name)
		}
		seen[f.Name] = true
		if f.Size < 0 || len(f.SHA256) != 64 {
			return fmt.Errorf("invalid attachment %q", f.Name)
		}
		total += f.Size
	}
	if total > MaxFiles {
		return fmt.Errorf("limit_exceeded: attachments are %d bytes; the limit is %d", total, MaxFiles)
	}
	return nil
}

// RenderIn is Render for a message stored in dir: one line per attachment,
// with its path on this host, between the header and the body.
func (e Envelope) RenderIn(dir string) string {
	var b strings.Builder
	b.WriteString(e.Header())
	for _, f := range e.Files {
		fmt.Fprintf(&b, "\nattached: %s (%d bytes)", filepath.Join(dir, "files", f.Name), f.Size)
	}
	b.WriteString("\n" + e.Text)
	return b.String()
}

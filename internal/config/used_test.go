package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review, 2026-11-03: four keys were in the table with nothing reading
// them, so setting them changed nothing. Every key must be named in
// non-test code outside this table. A backoff or deadline key counts when
// its prefix is read, as Settings.Backoff and Settings.Deadline do.
func TestEveryKeyIsRead(t *testing.T) {
	var src strings.Builder
	filepath.WalkDir("../..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") && !strings.HasSuffix(p, "config/keys.go") {
			b, _ := os.ReadFile(p)
			src.Write(b)
		}
		return nil
	})
	code := src.String()
	for _, k := range keys {
		name := k.Name
		read := strings.Contains(code, `"`+name+`"`)
		if i := strings.LastIndex(name, "."); !read && i > 0 {
			read = strings.Contains(code, `Backoff("`+name[:i]+`")`) || strings.Contains(code, `Deadline("`+name[:i]+`")`)
		}
		if !read {
			t.Errorf("%s: nothing reads it", name)
		}
	}
}

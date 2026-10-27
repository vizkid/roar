package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesSkill(t *testing.T) {
	root := t.TempDir()
	path, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "skills", "roar", "SKILL.md"); path != want {
		t.Fatalf("path %q, want %q", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != Content {
		t.Fatal("installed content differs from the embedded skill")
	}
	// The description is what makes an agent load the skill for a message.
	for _, s := range []string{"name: roar", "generated-by: roar", "[roar", "roar send --reply"} {
		if !strings.Contains(Content, s) {
			t.Errorf("skill lacks %q", s)
		}
	}
	if _, err := Install(root); err != nil {
		t.Fatalf("second install: %v", err)
	}
}

func TestInstallRewritesStaleCopy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "skills", "roar", "SKILL.md")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("---\nname: roar\nmetadata:\n  generated-by: roar\n---\nold\n"), 0o644)
	if _, err := Install(root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != Content {
		t.Fatal("stale Roar-written copy was not rewritten")
	}
}

func TestInstallLeavesOperatorCopy(t *testing.T) {
	t.Run("unmarked file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "skills", "roar", "SKILL.md")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("my own roar notes\n"), 0o644)
		if _, err := Install(root); !errors.Is(err, ErrOperatorManaged) {
			t.Fatalf("err %v, want ErrOperatorManaged", err)
		}
		if b, _ := os.ReadFile(path); string(b) != "my own roar notes\n" {
			t.Fatal("operator file was changed")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root, elsewhere := t.TempDir(), t.TempDir()
		os.MkdirAll(filepath.Join(root, "skills"), 0o755)
		if err := os.Symlink(elsewhere, filepath.Join(root, "skills", "roar")); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(root); !errors.Is(err, ErrOperatorManaged) {
			t.Fatalf("err %v, want ErrOperatorManaged", err)
		}
		if _, err := os.Stat(filepath.Join(elsewhere, "SKILL.md")); err == nil {
			t.Fatal("wrote through the operator's symlink")
		}
	})
}

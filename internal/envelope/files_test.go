package envelope

import (
	"strings"
	"testing"
)

func TestFileValidation(t *testing.T) {
	e := valid()
	e.Files = []File{FileFor("notes.txt", []byte("hello"))}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".hidden", "a/b", "..", strings.Repeat("x", 129)} {
		e.Files = []File{FileFor(name, []byte("x"))}
		if e.Validate() == nil {
			t.Errorf("accepted file name %q", name)
		}
	}
	e.Files = []File{{Name: "big", Size: MaxFiles + 1, SHA256: strings.Repeat("0", 64)}}
	if e.Validate() == nil {
		t.Error("accepted files over the limit")
	}
	e.Files = []File{FileFor("a", nil), FileFor("a", nil)}
	if e.Validate() == nil {
		t.Error("accepted a repeated name")
	}
}

func TestUniqueNames(t *testing.T) {
	got := UniqueNames([]string{"/x/a.txt", "/y/a.txt", "/z/b", "/w/a.txt"})
	want := []string{"a.txt", "a-2.txt", "b", "a-3.txt"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestRenderListsAttachedPaths(t *testing.T) {
	e := valid()
	e.Files = []File{FileFor("a.txt", []byte("12345"))}
	out := e.RenderIn("/store/box/b/rm-x")
	lines := strings.Split(out, "\n")
	if lines[1] != "attached: /store/box/b/rm-x/files/a.txt (5 bytes)" || !strings.HasSuffix(out, "\nhello") {
		t.Fatalf("render %q", out)
	}
	if strings.Contains(e.Render(), "attached:") {
		t.Fatal("Render without a directory must not invent paths")
	}
}

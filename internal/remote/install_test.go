package remote

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func crossBuild(t *testing.T, goos, goarch string) []byte {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "roar")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/roar")
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s/%s: %v %s", goos, goarch, err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The platform comes from the binary's own header, so a Mac can check a
// Linux build before it copies it.
func TestBinaryPlatform(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "Linux x86_64"},
		{"linux", "arm64", "Linux aarch64"},
		{"darwin", "arm64", "Darwin arm64"},
		{"darwin", "amd64", "Darwin x86_64"},
	} {
		if got, err := binaryPlatform(crossBuild(t, c.goos, c.goarch)); err != nil || got.uname() != c.want {
			t.Errorf("%s/%s: got %q %v, want %q", c.goos, c.goarch, got.uname(), err, c.want)
		}
	}
	if _, err := binaryPlatform([]byte("#!/bin/sh\necho hi\n")); err == nil {
		t.Error("a script passed as a binary")
	}
}

func TestInstallRefusesWrongPlatform(t *testing.T) {
	f := setup(t)
	goos := map[string]string{"darwin": "linux", "linux": "darwin"}[runtime.GOOS]
	err := f.host.Install(t.Context(), crossBuild(t, goos, "amd64"))
	for _, want := range []string{"GOOS=" + runtime.GOOS + " GOARCH=" + runtime.GOARCH, "--binary roar-" + runtime.GOOS + "-" + runtime.GOARCH + " "} {
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), want) {
			t.Fatalf("got %v, want a refusal naming %q", err, want)
		}
	}
}

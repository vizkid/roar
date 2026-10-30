package remote

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"fmt"
	"strings"
)

// platform is an operating system and architecture in Go's names.
type platform struct{ goos, goarch string }

// uname is the platform as `uname -sm` prints it.
func (p platform) uname() string {
	osName := map[string]string{"darwin": "Darwin", "linux": "Linux"}[p.goos]
	arch := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[p.goarch]
	if p.goos == "linux" && p.goarch == "arm64" {
		arch = "aarch64"
	}
	return osName + " " + arch
}

// unamePlatform reads `uname -sm` output back into Go's names.
func unamePlatform(s string) (platform, bool) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return platform{}, false
	}
	goos := map[string]string{"Darwin": "darwin", "Linux": "linux"}[f[0]]
	goarch := map[string]string{"x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[f[1]]
	return platform{goos, goarch}, goos != "" && goarch != ""
}

// binaryPlatform reads the platform from a Mach-O or ELF header, so a Mac
// can check a Linux build before it copies it.
func binaryPlatform(b []byte) (platform, error) {
	if f, err := macho.NewFile(bytes.NewReader(b)); err == nil {
		switch f.Cpu {
		case macho.CpuArm64:
			return platform{"darwin", "arm64"}, nil
		case macho.CpuAmd64:
			return platform{"darwin", "amd64"}, nil
		}
		return platform{}, fmt.Errorf("unsupported Mach-O CPU %v", f.Cpu)
	}
	if f, err := elf.NewFile(bytes.NewReader(b)); err == nil {
		switch f.Machine {
		case elf.EM_X86_64:
			return platform{"linux", "amd64"}, nil
		case elf.EM_AARCH64:
			return platform{"linux", "arm64"}, nil
		}
		return platform{}, fmt.Errorf("unsupported ELF machine %v", f.Machine)
	}
	return platform{}, fmt.Errorf("not a Mach-O or ELF binary")
}

// installScript decodes the binary from stdin into a fresh temporary file
// (mktemp creates it exclusively, so a planted symlink is never followed),
// then renames it over roar.
const installScript = `stty -echo; set -e; d="$HOME/.local/bin"; mkdir -p "$d"; ` +
	`t=$(mktemp "$d/.roar.XXXXXX"); trap 'rm -f "$t"' EXIT; base64 -d > "$t"; ` +
	`chmod 755 "$t"; mv -f "$t" "$d/roar"; trap - EXIT; echo ROAR-INSTALLED`

// Install copies binary to DefaultBin on the host. The binary's header must
// name the host's operating system and architecture.
func (h Host) Install(ctx context.Context, binary []byte) error {
	have, err := binaryPlatform(binary)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	code, screen, err := h.run(ctx, []string{"/usr/bin/uname", "-sm"}, nil, false)
	if err != nil {
		return err
	}
	host := strings.TrimSpace(screen)
	want, ok := unamePlatform(host)
	if code != 0 || !ok {
		return fmt.Errorf("%w: host reports %q, which Roar does not support", ErrRefused, host)
	}
	if have != want {
		return fmt.Errorf("%w: host is %q, the binary is %q; build one with "+
			"`CGO_ENABLED=0 GOOS=%s GOARCH=%s go build -o roar-%[4]s-%[5]s ./cmd/roar`, "+
			"then run `roar install --binary roar-%[4]s-%[5]s <host>`",
			ErrRefused, host, have.uname(), want.goos, want.goarch)
	}
	code, screen, err = h.run(ctx, []string{"/bin/sh", "-c", installScript}, binary, false)
	if err != nil {
		return err
	}
	if code != 0 || !strings.Contains(screen, "ROAR-INSTALLED") {
		return fmt.Errorf("%w: install exited %d: %s", ErrRefused, code, lastError(screen, code))
	}
	return nil
}

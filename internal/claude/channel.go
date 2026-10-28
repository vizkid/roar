// Package claude is Roar's native adapter for Claude Code: an MCP server
// that declares the channel capability once Claude's argv proves the channel
// flag, and receipt from the transcript.
package claude

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Server is the MCP server name and the channel source Claude shows.
const Server = "roar"

// ChannelArg loads the Roar channel. The equals form keeps a list-valued
// flag from swallowing the opening prompt; a listed form such as
// "--dangerously-load-development-channels server:penfold server:roar" also counts.
const ChannelArg = "--dangerously-load-development-channels=server:" + Server

const channelFlag = "--dangerously-load-development-channels"

// channelProof checks Claude's real argv. The adapter runs as Claude's MCP
// child, so its parent is Claude; a self-update can restart Claude without
// the flag, and only the live argv shows that.
func channelProof(readArgs func(pid int) ([]string, error), ppid int) (bool, string) {
	argv, err := readArgs(ppid)
	if err != nil {
		return false, fmt.Sprintf("parent_unreadable: cannot read the command line of pid %d: %v", ppid, err)
	}
	if printMode(argv) {
		return false, "print_mode: Claude print mode ignores channel notifications"
	}
	if !channelEnabled(argv) {
		return false, "channel_flag_missing: Claude was not started with " + ChannelArg
	}
	return true, ""
}

func printMode(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-p" || a == "--print" {
			return true
		}
	}
	return false
}

// channelEnabled looks for server:roar in the development-channels flag
// before any "--". A bare --channels does not load local servers.
func channelEnabled(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return false
		}
		flag, value, inline := strings.Cut(args[i], "=")
		if flag != channelFlag {
			continue
		}
		values := []string{value}
		if !inline {
			values = nil
			for j := i + 1; j < len(args) && !strings.HasPrefix(args[j], "-"); j++ {
				values = append(values, args[j])
			}
		}
		for _, list := range values {
			for _, item := range strings.Split(list, ",") {
				if item == "server:"+Server {
					return true
				}
			}
		}
	}
	return false
}

// ParentArgs reads a process's argv: /proc on Linux, ps elsewhere. ps is
// called by absolute path, because an MCP server gets no login PATH.
func ParentArgs(pid int) ([]string, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), nil
	}
	out, err := exec.Command("/bin/ps", "-o", "args=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

package poste

import (
	"context"
	"strconv"
	"strings"
)

// Memory returns the memory of the machine in bytes (sysctl hw.memsize).
func Memory() (uint64, bool) {
	out, _, err := run(context.Background(), "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, false
	}
	return parseMemsize(out)
}

func parseMemsize(out string) (uint64, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil || n == 0 || n > 1<<54 {
		return 0, false
	}
	return n, true
}

// WSL: never on macOS.
func WSL() bool { return false }

// placeholder: macOS ships /usr/bin/git, make and python3 as stand-ins that
// only offer to install the developer tools. They count as present when
// those tools are installed; the stand-in itself is never launched.
func placeholder(path string) bool {
	if !strings.HasPrefix(path, "/usr/bin/") {
		return false
	}
	switch strings.TrimPrefix(path, "/usr/bin/") {
	case "git", "make", "python3":
	default:
		return false
	}
	_, _, err := run(context.Background(), "/usr/bin/xcode-select", "-p")
	return err != nil
}

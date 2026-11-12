package poste

import (
	"os"
	"strconv"
	"strings"
)

// Memory returns the memory of the machine in bytes (MemTotal of /proc/meminfo).
func Memory() (uint64, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	return parseMeminfo(string(raw))
}

func parseMeminfo(text string) (uint64, bool) {
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemTotal:" && f[2] == "kB" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil || kb == 0 || kb > 1<<44 {
				return 0, false
			}
			return kb << 10, true
		}
	}
	return 0, false
}

// WSL tells whether this Linux runs under the Windows Subsystem for Linux.
func WSL() bool {
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && wslRelease(string(raw))
}

func wslRelease(release string) bool {
	r := strings.ToLower(release)
	return strings.Contains(r, "microsoft") || strings.Contains(r, "wsl")
}

func placeholder(string) bool { return false }

package oracle

import "syscall"

// machineArch reads the architecture of the Mac: hw.optional.arm64 is 1 on
// Apple silicon even for a translated process, hw.machine otherwise.
func machineArch() string {
	if v, err := syscall.SysctlUint32("hw.optional.arm64"); err == nil && v == 1 {
		return "arm64"
	}
	if m, err := syscall.Sysctl("hw.machine"); err == nil && m != "" {
		return normalizeArch(m)
	}
	return ""
}

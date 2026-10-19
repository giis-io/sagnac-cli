package oracle

import "syscall"

// machineArch reads the machine field of uname.
func machineArch() string {
	var u syscall.Utsname
	if syscall.Uname(&u) != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Machine))
	for _, c := range u.Machine {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return normalizeArch(string(b))
}

package oracle

import (
	"syscall"
	"unsafe"
)

// liftLoopback brings lo up (SIOCGIFFLAGS, SIOCSIFFLAGS) in the network
// namespace of the process: ip may be missing on the machine.
func liftLoopback() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var ifr struct {
		name  [syscall.IFNAMSIZ]byte
		flags uint16
		_     [24 - 2]byte
	}
	copy(ifr.name[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return e
	}
	if ifr.flags&syscall.IFF_UP != 0 {
		return nil
	}
	ifr.flags |= syscall.IFF_UP
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return e
	}
	return nil
}

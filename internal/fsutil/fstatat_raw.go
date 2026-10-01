//go:build linux && (amd64 || ppc64 || ppc64le || s390x)

package fsutil

import (
	"syscall"
	"unsafe"
)

// fstatat is newfstatat(2) into Stat_t, as the syscall package does it
// internally on these architectures; it does not export it there.
func fstatat(dfd int, name string, st *syscall.Stat_t, flags int) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_NEWFSTATAT, uintptr(dfd), uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(st)), uintptr(flags), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

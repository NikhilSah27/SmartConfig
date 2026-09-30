//go:build linux && amd64

package fsutil

import (
	"syscall"
	"unsafe"
)

// fstatat is newfstatat(2); the syscall package does not export it on
// amd64.
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

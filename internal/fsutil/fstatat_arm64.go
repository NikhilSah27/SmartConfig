//go:build linux && arm64

package fsutil

import "syscall"

func fstatat(dfd int, name string, st *syscall.Stat_t, flags int) error {
	return syscall.Fstatat(dfd, name, st, flags)
}

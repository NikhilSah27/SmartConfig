//go:build linux && (arm64 || riscv64 || loong64)

package fsutil

import "syscall"

func fstatat(dfd int, name string, st *syscall.Stat_t, flags int) error {
	return syscall.Fstatat(dfd, name, st, flags)
}

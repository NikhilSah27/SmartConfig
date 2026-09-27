//go:build linux

package fsutil

import (
	"os"
	"syscall"
)

func owner(fi os.FileInfo) (uid, gid int) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(st.Uid), int(st.Gid)
}

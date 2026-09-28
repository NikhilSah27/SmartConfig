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

func stampOf(fi os.FileInfo) Stamp {
	st := Stamp{Size: fi.Size(), Mode: fi.Mode(), Mtime: fi.ModTime().UnixNano()}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.Dev, st.Ino = uint64(sys.Dev), sys.Ino
		st.Ctime = sys.Ctim.Sec*1e9 + sys.Ctim.Nsec
		st.UID, st.GID = int(sys.Uid), int(sys.Gid)
	}
	return st
}

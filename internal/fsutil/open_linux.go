//go:build linux

package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// syncDir fsyncs a directory. It opens it with O_DIRECTORY|O_NONBLOCK, so
// if something other than a directory (a FIFO, say) has been put in its
// place, the open fails at once instead of blocking.
func syncDir(dir string) error {
	d, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Inode flags of FS_IOC_GETFLAGS (linux/fs.h).
const (
	fsIocGetflags = 0x80086601
	fsImmutableFl = 0x00000010
	fsAppendFl    = 0x00000020
)

// ReplaceRefused returns why path cannot be replaced or removed, as a
// restore does: it, or its directory, is immutable (chattr +i) or
// append-only (chattr +a). The rename would fail anyway; this says why
// before anything is written. A filesystem without these flags, or a
// path that is not there, refuses nothing.
func ReplaceRefused(path string) error {
	for _, p := range []string{path, filepath.Dir(path)} {
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		var flags uint64 // the kernel writes an int; 8 bytes leave room either way
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fsIocGetflags, uintptr(unsafe.Pointer(&flags)))
		f.Close()
		if errno != 0 {
			continue
		}
		switch {
		case flags&fsImmutableFl != 0:
			return fmt.Errorf("%s is immutable (chattr +i), so it cannot be replaced: run chattr -i %s first", p, p)
		case flags&fsAppendFl != 0:
			return fmt.Errorf("%s is append-only (chattr +a), so it cannot be replaced: run chattr -a %s first", p, p)
		}
	}
	return nil
}

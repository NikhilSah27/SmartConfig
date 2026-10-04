//go:build linux

package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Inode flags of FS_IOC_GETFLAGS (linux/fs.h); the request number is per
// architecture (ioctl_*.go).
const (
	fsImmutableFl = 0x00000010
	fsAppendFl    = 0x00000020
)

// ReplaceRefused returns why path cannot be replaced or removed, as a
// restore does: it, or its directory, is immutable (chattr +i) or
// append-only (chattr +a). The rename would fail anyway; this says why,
// every flag at once, before anything is written. Only a regular file or
// a directory is opened, never a FIFO or a device. A filesystem without
// these flags refuses nothing, nor does a path that is not there (its
// directory is still looked at).
func ReplaceRefused(path string) error {
	var why, fix []string
	for _, p := range []string{path, filepath.Dir(path)} {
		attrs := inodeFlags(p)
		if attrs == "" {
			continue
		}
		what := map[string]string{"i": "immutable", "a": "append-only", "ia": "immutable and append-only"}[attrs]
		why = append(why, fmt.Sprintf("%s is %s (chattr +%s)", p, what, attrs))
		fix = append(fix, fmt.Sprintf("chattr -%s %s", attrs, p))
	}
	if len(why) == 0 {
		return nil
	}
	return fmt.Errorf("%s, so %s cannot be replaced: run %s first", strings.Join(why, " and "), path, strings.Join(fix, " and "))
}

// inodeFlags is "i", "a", "ia" or "" for p's immutable and append-only
// flags.
func inodeFlags(p string) string {
	fi, err := os.Lstat(p)
	if err != nil || !(fi.Mode().IsRegular() || fi.IsDir()) {
		return ""
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	var flags uint32 // the kernel writes an int, whatever the request's size says
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fsIocGetflags, uintptr(unsafe.Pointer(&flags))); errno != 0 {
		return ""
	}
	out := ""
	if flags&fsImmutableFl != 0 {
		out += "i"
	}
	if flags&fsAppendFl != 0 {
		out += "a"
	}
	return out
}

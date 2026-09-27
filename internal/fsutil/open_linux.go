//go:build linux

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a symlink in the last
// path component, and without blocking if a FIFO has been put in its place.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		// O_NOFOLLOW reports a symlink as the last component with ELOOP, but
		// so is a loop in a directory component. Only the first is "a symlink".
		if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symlink, refusing", path)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return f, nil
}

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

//go:build linux

package fsutil

import (
	"os"
	"syscall"
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

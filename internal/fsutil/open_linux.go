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
		return nil, fmt.Errorf("%s is a symlink, refusing", path)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return f, nil
}

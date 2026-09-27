// Package fsutil reads config files with their metadata and writes them back
// atomically.
package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxSize is the largest file SmartConfig will snapshot or restore.
const MaxSize = 8 << 20

// Meta is the ownership and permission information recorded for a file.
type Meta struct {
	Mode os.FileMode
	UID  int
	GID  int
}

// CheckRegular refuses anything that is not a regular file of at most MaxSize
// bytes. Symlinks are refused rather than followed.
func CheckRegular(path string, fi os.FileInfo) error {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink, refusing", path)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	case fi.Size() > MaxSize:
		return fmt.Errorf("%s is %d bytes, larger than the 8 MB limit", path, fi.Size())
	}
	return nil
}

// ReadWithMeta returns the contents of a regular file with its mode and owner.
func ReadWithMeta(path string) ([]byte, Meta, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, Meta{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := CheckRegular(path, fi); err != nil {
		return nil, Meta{}, err
	}
	return readChecked(path, fi)
}

// readChecked reads path only if it is still the file that lstat described as
// want. Between the lstat and the open, the path may have been replaced by a
// symlink (which would make root read whatever it points to) or by a FIFO
// (which would block the read forever); both are refused.
func readChecked(path string, want os.FileInfo) ([]byte, Meta, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, Meta{}, err
	}
	defer f.Close()
	// Stat the open file so the metadata matches the bytes we read.
	fi, err := f.Stat()
	if err != nil {
		return nil, Meta{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if !os.SameFile(want, fi) {
		return nil, Meta{}, fmt.Errorf("%s was replaced while being read, try again", path)
	}
	if err := CheckRegular(path, fi); err != nil {
		return nil, Meta{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, Meta{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > MaxSize {
		return nil, Meta{}, fmt.Errorf("%s grew past the 8 MB limit while reading", path)
	}
	uid, gid := owner(fi)
	return data, Meta{Mode: fi.Mode(), UID: uid, GID: gid}, nil
}

// WriteAtomic replaces path with data: it writes a temp file in the same
// directory, sets mode and owner, fsyncs, and renames it over the original.
// On failure the temp file is removed and the original is left untouched.
func WriteAtomic(path string, data []byte, mode os.FileMode, uid, gid int) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".sc-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	// Chown before chmod: chown clears setuid/setgid bits on Linux.
	if err = tmp.Chown(uid, gid); err != nil {
		return fmt.Errorf("chown %s to %d:%d: %w", tmpName, uid, gid, err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod %s to %04o: %w", tmpName, mode.Perm(), err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("fsync %s: %w", tmpName, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename over %s: %w", path, err)
	}
	// Make the rename itself durable. The file is already in place, so a
	// failure here is not reported as a failed write.
	if d, derr := os.Open(dir); derr == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// IsNotExist reports whether err means the file does not exist.
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

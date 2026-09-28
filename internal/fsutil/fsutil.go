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

// Meta is the ownership and permission information recorded for a file,
// plus the stamp of the exact version that was read.
type Meta struct {
	Mode  os.FileMode
	UID   int
	GID   int
	Stamp Stamp
}

// Stamp identifies one version of a file cheaply: if any field differs
// between two stats, the file was replaced or changed in between (a rename
// over it gives a new inode; a write changes size, mtime or ctime; chmod and
// chown change ctime).
type Stamp struct {
	Dev, Ino     uint64
	Size         int64
	Mtime, Ctime int64 // nanoseconds
	Mode         os.FileMode
	UID, GID     int
}

// LstatStamp returns the stamp of path without following a symlink.
func LstatStamp(path string) (Stamp, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Stamp{}, err
	}
	return stampOf(fi), nil
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
	return data, Meta{Mode: fi.Mode(), UID: uid, GID: gid, Stamp: stampOf(fi)}, nil
}

// WriteAtomic replaces path with data: it writes a temp file in the same
// directory, sets mode and owner, fsyncs, and renames it over the original.
// On failure the temp file is removed and the original is left untouched.
func WriteAtomic(path string, data []byte, mode os.FileMode, uid, gid int) error {
	p, err := PrepareAtomic(path, data, mode, uid, gid)
	if err != nil {
		return err
	}
	if err := p.Commit(); err != nil {
		p.Discard()
		return err
	}
	return nil
}

// Pending is a temp file next to its target that already holds the complete
// new content, with mode and owner set and synced to disk. Nothing visible
// has changed until Commit renames it over the target.
type Pending struct {
	path, tmp string
	done      bool
}

// PrepareAtomic does the slow part of WriteAtomic (write, chown, chmod,
// fsync) and returns the temp file for Commit or Discard. Callers that must
// hold a lock across the replacement can prepare first and lock only around
// Commit.
func PrepareAtomic(path string, data []byte, mode os.FileMode, uid, gid int) (p *Pending, err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".sc-tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return nil, fmt.Errorf("write %s: %w", tmpName, err)
	}
	// Chown before chmod: chown clears setuid/setgid bits on Linux.
	if err = tmp.Chown(uid, gid); err != nil {
		return nil, fmt.Errorf("chown %s to %d:%d: %w", tmpName, uid, gid, err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return nil, fmt.Errorf("chmod %s to %04o: %w", tmpName, mode.Perm(), err)
	}
	if err = tmp.Sync(); err != nil {
		return nil, fmt.Errorf("fsync %s: %w", tmpName, err)
	}
	if err = tmp.Close(); err != nil {
		return nil, fmt.Errorf("close %s: %w", tmpName, err)
	}
	return &Pending{path: path, tmp: tmpName}, nil
}

// Commit renames the temp file over the target, then syncs the directory so
// the rename itself is durable. The file is already in place once the rename
// succeeds, so a failed directory sync is not reported as a failed write.
func (p *Pending) Commit() error {
	if err := os.Rename(p.tmp, p.path); err != nil {
		return fmt.Errorf("rename over %s: %w", p.path, err)
	}
	p.done = true
	syncDir(filepath.Dir(p.path))
	return nil
}

// Discard removes the temp file unless it has been committed. It is safe to
// call more than once, and after Commit.
func (p *Pending) Discard() {
	if !p.done {
		os.Remove(p.tmp)
		p.done = true
	}
}

// IsNotExist reports whether err means the file does not exist.
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

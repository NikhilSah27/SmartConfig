// Package fsutil reads config files with their metadata and writes them back
// atomically.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MaxSize is the largest file SmartConfig will snapshot or restore.
const MaxSize = 8 << 20

// ErrReplaced means the file at a path was replaced between being checked
// and being opened; reading it again is safe.
var ErrReplaced = errors.New("was replaced while being read, try again")

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

// ReadWithMeta returns the contents of a regular file with its mode and
// owner. It is ReadState for regular files only: a symlink is refused.
func ReadWithMeta(path string) ([]byte, Meta, error) {
	s, err := ReadState(path)
	if err != nil {
		return nil, Meta{}, err
	}
	if s.Kind == "link" {
		return nil, Meta{}, fmt.Errorf("%s is a symlink, refusing", path)
	}
	return s.Data, s.Meta, nil
}

// readChecked reads path only if it is still the file that lstat described as
// want. Between the lstat and the open, the path may have been replaced by a
// symlink (which would make root read whatever it points to) or by a FIFO
// (which would block the read forever); both are refused.
func readChecked(path string, want os.FileInfo) ([]byte, Meta, error) {
	dfd, name, err := openParent(path)
	if err != nil {
		return nil, Meta{}, err
	}
	defer syscall.Close(dfd)
	s, err := readFileAt(dfd, name, path, func(fi os.FileInfo) bool { return os.SameFile(want, fi) })
	if err != nil {
		return nil, Meta{}, err
	}
	return s.Data, s.Meta, nil
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

// pendingTemps holds the temp files PrepareAtomic created that are not yet
// committed or discarded, so an interrupted sc can remove them.
var pendingTemps = struct {
	sync.Mutex
	names  map[string]bool
	dirs   map[string]bool // private directories of this process (TrackDir)
	closed bool
}{names: map[string]bool{}, dirs: map[string]bool{}}

// trackTemp records a new temp file. After RemovePending has run, the
// process is exiting: the file is removed at once and false is returned.
func trackTemp(name string) bool {
	pendingTemps.Lock()
	defer pendingTemps.Unlock()
	if pendingTemps.closed {
		os.Remove(name)
		return false
	}
	pendingTemps.names[name] = true
	return true
}

func untrackTemp(name string) {
	pendingTemps.Lock()
	delete(pendingTemps.names, name)
	pendingTemps.Unlock()
}

// RemovePending deletes every temp file of this process that was prepared
// but not yet committed or discarded, and makes later PrepareAtomic calls
// fail, so nothing new is left behind while the process exits. It is meant
// for signal handlers, which exit without running deferred cleanups. It
// never touches a target file.
func RemovePending() {
	pendingTemps.Lock()
	defer pendingTemps.Unlock()
	pendingTemps.closed = true
	for name := range pendingTemps.names {
		os.Remove(name)
		delete(pendingTemps.names, name)
	}
	for dir := range pendingTemps.dirs {
		os.RemoveAll(dir)
		delete(pendingTemps.dirs, dir)
	}
}

// TrackDir records a private directory this process removes when it is
// done with it (UntrackDir), so that RemovePending removes it too when a
// signal ends the process first. After RemovePending has run, the
// directory is removed at once and false is returned.
func TrackDir(dir string) bool {
	pendingTemps.Lock()
	defer pendingTemps.Unlock()
	if pendingTemps.closed {
		os.RemoveAll(dir)
		return false
	}
	pendingTemps.dirs[dir] = true
	return true
}

// UntrackDir forgets a directory TrackDir recorded.
func UntrackDir(dir string) {
	pendingTemps.Lock()
	delete(pendingTemps.dirs, dir)
	pendingTemps.Unlock()
}

// staleAfter is the age past which a private directory is a leftover of
// a process killed before its cleanup (SIGKILL, a power cut): sc's are
// in use for seconds, a read piped into a pager for minutes.
const staleAfter = time.Hour

// TempParents are the places for sc's private temporary directories, in
// the order tried. A read-only root (the rescue shell) leaves only the
// tmpfs ones: /run and /dev/shm for root; for a user, their own runtime
// directory ($XDG_RUNTIME_DIR, if it is theirs), $TMPDIR and /dev/shm.
func TempParents() []string {
	tmp := os.TempDir()
	if os.Geteuid() == 0 {
		return []string{"/run", "/dev/shm", tmp}
	}
	var out []string
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && ownedByMe(fi) {
			out = append(out, d)
		}
	}
	return append(out, tmp, "/dev/shm")
}

func ownedByMe(fi os.FileInfo) bool {
	uid, _ := owner(fi)
	return uid == os.Geteuid()
}

// PrivateDir creates a new directory prefix* (mode 0700) in the first of
// parents where it can, after removing this user's prefix* directories
// there that are older than an hour. It is tracked (TrackDir); the caller
// removes it with RemoveDir. With none possible, the error names every
// place tried and why.
func PrivateDir(prefix string, parents []string) (string, error) {
	var tried []string
	for _, parent := range parents {
		sweep(parent, prefix)
		dir, err := os.MkdirTemp(parent, prefix)
		if err == nil {
			TrackDir(dir)
			return dir, nil
		}
		tried = append(tried, fmt.Sprintf("%s (%s)", parent, ErrText(err)))
	}
	return "", fmt.Errorf("no writable place for a private directory: %s", strings.Join(tried, ", "))
}

// RemoveDir removes a directory PrivateDir made and forgets it.
func RemoveDir(dir string) {
	os.RemoveAll(dir)
	UntrackDir(dir)
}

// sweep removes this user's prefix* directories in parent that are older
// than staleAfter. It never follows a link: RemoveAll removes one, not
// what it names.
func sweep(parent, prefix string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && ownedByMe(fi) && time.Since(fi.ModTime()) > staleAfter {
			os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
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
	if !trackTemp(tmpName) {
		tmp.Close()
		return nil, fmt.Errorf("create temp file in %s: interrupted", dir)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
			untrackTemp(tmpName)
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
	untrackTemp(p.tmp)
	p.done = true
	syncDir(filepath.Dir(p.path))
	return nil
}

// Discard removes the temp file unless it has been committed. It is safe to
// call more than once, and after Commit.
func (p *Pending) Discard() {
	if !p.done {
		os.Remove(p.tmp)
		untrackTemp(p.tmp)
		p.done = true
	}
}

// IsNotExist reports whether err means the file does not exist.
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

// ErrText is err without the operation and path an *fs.PathError
// repeats: "permission denied", for a message that names the path itself.
func ErrText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

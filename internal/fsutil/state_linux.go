//go:build linux

package fsutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ErrNotRecordable is matched (errors.Is) by ReadState's error for anything
// that is neither a symlink nor a regular file of at most MaxSize bytes:
// directories, FIFOs, sockets, devices and big files.
var ErrNotRecordable = errors.New("cannot be recorded")

// ErrTooBig is matched, besides ErrNotRecordable, for a regular file over
// MaxSize: unlike a directory or a FIFO at the path, the file is there.
var ErrTooBig = errors.New("larger than the 8 MB limit")

type notRecordable struct {
	msg string
	big bool
}

func (e *notRecordable) Error() string { return e.msg }
func (e *notRecordable) Is(target error) bool {
	return target == ErrNotRecordable || e.big && target == ErrTooBig
}

// State is one observed version of a path: a regular file with its content,
// or a symlink with its target text. Meta holds the mode and owner (of the
// link itself for a symlink) and the stamp of the version read.
type State struct {
	Kind   string // "file" or "link"
	Data   []byte // file content
	Target string // link text
	Meta   Meta
	Stable bool // false: the path changed while it was being read
}

// linkRefused is readFileAt's error for a symlink where a file was
// expected. ReadState turns it into ErrReplaced; readChecked reports it.
type linkRefused struct{ path string }

func (e *linkRefused) Error() string { return e.path + " is a symlink, refusing" }

// testHookBeforeOpen, if set by a test, runs in ReadState after fstatat
// and before the file is opened or the link read.
var testHookBeforeOpen func()

// testHookAfterRead, if set by a test, runs in ReadState after the content
// or link text is read and before the path is checked again.
var testHookAfterRead func()

// ReadState reads path without ever following a symlink in it: the parent
// directory is opened with O_DIRECTORY|O_NOFOLLOW (a symlinked parent is
// refused), the name is examined with fstatat, a symlink's text is read
// with readlinkat, and a regular file is opened with
// openat(O_NOFOLLOW|O_NONBLOCK), whose fstat must be the file fstatat saw.
// An absent path gives an error for which IsNotExist holds. Stable is false
// when the path changed between the first look and the end of the read.
func ReadState(path string) (State, error) {
	dfd, name, err := openParent(path)
	if err != nil {
		return State{}, err
	}
	defer syscall.Close(dfd)
	return readAt(dfd, name, path)
}

// ErrUnsafePath is matched by ReadStateBelow's error when a directory
// between the base directory and the file is a symlink: nothing is read
// through it.
var ErrUnsafePath = errors.New("a symlink below the watched directory, refusing")

// ReadStateBelow is ReadState for a path below dir, a directory whose
// contents a user may control (a login's ~/.ssh): dir and every directory
// from it down to the file's parent are opened with O_NOFOLLOW, one at a
// time with openat, so a symlink anywhere below dir, or dir itself swapped
// for one, is refused (ErrUnsafePath) and never followed. A path that no
// longer exists, also because a directory on the way became a file, gives
// an error for which IsNotExist holds.
func ReadStateBelow(dir, path string) (State, error) {
	dfd, name, err := openParentBelow(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return State{}, err
	}
	defer syscall.Close(dfd)
	return readAt(dfd, name, path)
}

const dirFlags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC

func openParentBelow(dir, path string) (int, string, error) {
	rel, ok := strings.CutPrefix(path, dir+"/")
	if !ok || rel == "" {
		return -1, "", fmt.Errorf("read %s: not below %s", path, dir)
	}
	parts := strings.Split(rel, "/")
	fd, err := syscall.Open(dir, dirFlags, 0)
	if err != nil {
		return -1, "", belowErr(path, dir, -1, dir, err)
	}
	cur := dir
	for _, c := range parts[:len(parts)-1] {
		nfd, err := syscall.Openat(fd, c, dirFlags, 0)
		if err != nil {
			err = belowErr(path, cur+"/"+c, fd, c, err)
			syscall.Close(fd)
			return -1, "", err
		}
		syscall.Close(fd)
		fd, cur = nfd, cur+"/"+c
	}
	return fd, parts[len(parts)-1], nil
}

// belowErr explains why directory name (in dfd, or a path when dfd < 0)
// did not open: a symlink is unsafe, anything else not a directory means
// the path is gone.
func belowErr(path, shown string, dfd int, name string, err error) error {
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		var st syscall.Stat_t
		var serr error
		if dfd < 0 {
			serr = syscall.Lstat(name, &st)
		} else {
			serr = fstatat(dfd, name, &st, _AT_SYMLINK_NOFOLLOW)
		}
		if serr == nil && st.Mode&syscall.S_IFMT == syscall.S_IFLNK {
			return fmt.Errorf("%s: %s is %w", path, shown, ErrUnsafePath)
		}
		return fmt.Errorf("read %s: %w", path, os.ErrNotExist)
	}
	return fmt.Errorf("read %s: %w", path, err)
}

// readAt reads name in the open directory dfd (ReadState's work).
func readAt(dfd int, name, path string) (State, error) {
	var st syscall.Stat_t
	if err := fstatat(dfd, name, &st, _AT_SYMLINK_NOFOLLOW); err != nil {
		return State{}, fmt.Errorf("read %s: %w", path, err)
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFLNK:
		return readLink(dfd, name, path, &st)
	case syscall.S_IFREG:
		if st.Size > MaxSize {
			return State{}, &notRecordable{fmt.Sprintf("%s is %d bytes, larger than the 8 MB limit", path, st.Size), true}
		}
		s, err := readFileAt(dfd, name, path, func(fi os.FileInfo) bool { return sameStat(fi, &st) })
		var link *linkRefused
		if errors.As(err, &link) {
			// A link put in its place since fstatat: read the path again.
			return State{}, fmt.Errorf("%s %w", path, ErrReplaced)
		}
		return s, err
	default:
		return State{}, &notRecordable{msg: fmt.Sprintf("%s is not a regular file", path)}
	}
}

// openParent opens the directory holding path with O_NOFOLLOW and returns
// it with the path's last component. A symlinked parent is refused, but a
// symlink loop is reported as the loop it is.
func openParent(path string) (int, string, error) {
	path = filepath.Clean(path)
	// Dir, not Split: a trailing "/" would make the kernel follow a
	// symlinked directory even with O_NOFOLLOW.
	dir, name := filepath.Dir(path), filepath.Base(path)
	if name == "/" || name == "." || name == ".." {
		return -1, "", fmt.Errorf("read %s: not a file path", path)
	}
	dfd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err == nil {
		return dfd, name, nil
	}
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		// O_NOFOLLOW gives ELOOP (or ENOTDIR with O_DIRECTORY) for a
		// symlink as the last component, and ELOOP for a real loop too.
		_, serr := os.Stat(dir)
		if serr != nil {
			// The directory does not resolve at all (a loop, say):
			// report why.
			return -1, "", fmt.Errorf("read %s: %w", path, serr)
		}
		if fi, lerr := os.Lstat(dir); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return -1, "", fmt.Errorf("%s: its directory %s is a symlink, refusing", path, dir)
		}
	}
	return -1, "", fmt.Errorf("read %s: open %s: %w", path, dir, err)
}

func readLink(dfd int, name, path string, before *syscall.Stat_t) (State, error) {
	if testHookBeforeOpen != nil {
		testHookBeforeOpen()
	}
	target, err := readlinkat(dfd, name)
	if errors.Is(err, syscall.EINVAL) {
		// No longer a link: something was put in its place since fstatat.
		return State{}, fmt.Errorf("%s %w", path, ErrReplaced)
	}
	if err != nil {
		return State{}, fmt.Errorf("read link %s: %w", path, err)
	}
	if testHookAfterRead != nil {
		testHookAfterRead()
	}
	fi, err := statInfo(name, before)
	if err != nil {
		return State{}, err
	}
	// Compare stamps, not whole stats: readlink may update the access time.
	var after syscall.Stat_t
	stable := false
	if fstatat(dfd, name, &after, _AT_SYMLINK_NOFOLLOW) == nil {
		if fi2, err := statInfo(name, &after); err == nil && stampOf(fi2) == stampOf(fi) {
			stable = true
		}
	}
	uid, gid := owner(fi)
	return State{Kind: "link", Target: target, Stable: stable,
		Meta: Meta{Mode: fi.Mode(), UID: uid, GID: gid, Stamp: stampOf(fi)}}, nil
}

// readFileAt opens name in dfd without following a symlink or blocking on a
// FIFO, checks with same that it is the file expected, and reads it.
func readFileAt(dfd int, name, path string, same func(os.FileInfo) bool) (State, error) {
	if testHookBeforeOpen != nil {
		testHookBeforeOpen()
	}
	fd, err := syscall.Openat(dfd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return State{}, &linkRefused{path}
		}
		return State{}, fmt.Errorf("read %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return State{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if !same(fi) {
		return State{}, fmt.Errorf("%s %w", path, ErrReplaced)
	}
	if err := CheckRegular(path, fi); err != nil {
		return State{}, &notRecordable{err.Error(), fi.Mode().IsRegular()}
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return State{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > MaxSize {
		return State{}, &notRecordable{fmt.Sprintf("%s grew past the 8 MB limit while reading", path), true}
	}
	if testHookAfterRead != nil {
		testHookAfterRead()
	}
	stamp := stampOf(fi)
	stable := false
	if fi2, err := f.Stat(); err == nil && stampOf(fi2) == stamp {
		var st syscall.Stat_t
		if fstatat(dfd, name, &st, _AT_SYMLINK_NOFOLLOW) == nil && sameStat(fi, &st) {
			if fi3, err := statInfo(name, &st); err == nil && stampOf(fi3) == stamp {
				stable = true
			}
		}
	}
	uid, gid := owner(fi)
	return State{Kind: "file", Data: data, Stable: stable,
		Meta: Meta{Mode: fi.Mode(), UID: uid, GID: gid, Stamp: stamp}}, nil
}

// sameStat reports whether fi and st describe the same file: device,
// inode and type (a filesystem may give a new file a removed one's inode).
func sameStat(fi os.FileInfo, st *syscall.Stat_t) bool {
	s, ok := fi.Sys().(*syscall.Stat_t)
	return ok && s.Dev == st.Dev && s.Ino == st.Ino && s.Mode&syscall.S_IFMT == st.Mode&syscall.S_IFMT
}

// statInfo turns a Stat_t from fstatat into an os.FileInfo.
func statInfo(name string, st *syscall.Stat_t) (os.FileInfo, error) {
	return &statFileInfo{name: name, st: *st}, nil
}

// SymlinkAtomic makes path a symlink to target owned by uid:gid, replacing
// whatever file or link is there in one rename: it creates the link as
// ".<base>.sc-tmp-<random>" in the same directory, sets its owner with
// lchown, renames it over path and syncs the directory. The parent
// directory must not be a symlink. On failure the temp link is removed.
func SymlinkAtomic(path, target string, uid, gid int) error {
	dfd, name, err := openParent(path)
	if err != nil {
		return err
	}
	defer syscall.Close(dfd)
	var tmp string
	for attempt := 0; ; attempt++ {
		tmp = "." + name + ".sc-tmp-" + tempSuffix()
		err = symlinkat(target, dfd, tmp)
		if !errors.Is(err, syscall.EEXIST) || attempt == 9 {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("create symlink in %s: %w", filepath.Dir(path), err)
	}
	full := filepath.Join(filepath.Dir(path), tmp)
	if !trackTemp(full) {
		syscall.Unlinkat(dfd, tmp)
		return fmt.Errorf("create symlink in %s: interrupted", filepath.Dir(path))
	}
	defer untrackTemp(full)
	if err := syscall.Fchownat(dfd, tmp, uid, gid, _AT_SYMLINK_NOFOLLOW); err != nil {
		syscall.Unlinkat(dfd, tmp)
		return fmt.Errorf("chown symlink %s to %d:%d: %w", full, uid, gid, err)
	}
	if testHookBeforeSymlinkRename != nil {
		testHookBeforeSymlinkRename(full)
	}
	if err := syscall.Renameat(dfd, tmp, dfd, name); err != nil {
		syscall.Unlinkat(dfd, tmp)
		return fmt.Errorf("rename symlink over %s: %w", path, err)
	}
	syscall.Fsync(dfd)
	return nil
}

// testHookBeforeSymlinkRename, if set by a test, runs in SymlinkAtomic with
// the temp link's path after its owner is set and before the rename.
var testHookBeforeSymlinkRename func(tmp string)

// RemoveFile unlinks the file or symlink at path and syncs its directory.
// It refuses a directory, and a path whose parent directory is a symlink.
func RemoveFile(path string) error {
	dfd, name, err := openParent(path)
	if err != nil {
		return err
	}
	defer syscall.Close(dfd)
	// unlinkat without AT_REMOVEDIR never removes a directory (EISDIR).
	if err := syscall.Unlinkat(dfd, name); err != nil {
		if errors.Is(err, syscall.EISDIR) {
			return fmt.Errorf("%s is a directory, refusing to remove it", path)
		}
		return fmt.Errorf("remove %s: %w", path, err)
	}
	syscall.Fsync(dfd)
	return nil
}

// tempSuffix makes the random part of a temp link's name; tests replace
// it to force a collision.
var tempSuffix = randomSuffix

func randomSuffix() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// _AT_SYMLINK_NOFOLLOW is AT_SYMLINK_NOFOLLOW from <fcntl.h>, which the
// syscall package does not export on every architecture.
const _AT_SYMLINK_NOFOLLOW = 0x100

// readlinkat and symlinkat are not exported by the syscall package.
func readlinkat(dfd int, name string) (string, error) {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return "", err
	}
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, _, errno := syscall.Syscall6(syscall.SYS_READLINKAT, uintptr(dfd), uintptr(unsafe.Pointer(p)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(size), 0, 0)
		if errno != 0 {
			return "", errno
		}
		if int(n) < size {
			return string(buf[:n]), nil
		}
	}
}

func symlinkat(target string, dfd int, name string) error {
	t, err := syscall.BytePtrFromString(target)
	if err != nil {
		return err
	}
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_SYMLINKAT, uintptr(unsafe.Pointer(t)), uintptr(dfd), uintptr(unsafe.Pointer(p)))
	if errno != 0 {
		return errno
	}
	return nil
}

// statFileInfo is an os.FileInfo for a Stat_t from fstatat, with the mode
// converted exactly as the os package does, so its stamp equals the stamp
// of the same file from os.Lstat.
type statFileInfo struct {
	name string
	st   syscall.Stat_t
}

func (f *statFileInfo) Name() string       { return f.name }
func (f *statFileInfo) Size() int64        { return f.st.Size }
func (f *statFileInfo) ModTime() time.Time { return time.Unix(f.st.Mtim.Sec, f.st.Mtim.Nsec) }
func (f *statFileInfo) IsDir() bool        { return f.Mode().IsDir() }
func (f *statFileInfo) Sys() any           { return &f.st }

func (f *statFileInfo) Mode() os.FileMode {
	m := os.FileMode(f.st.Mode & 0o777)
	switch f.st.Mode & syscall.S_IFMT {
	case syscall.S_IFBLK:
		m |= os.ModeDevice
	case syscall.S_IFCHR:
		m |= os.ModeDevice | os.ModeCharDevice
	case syscall.S_IFDIR:
		m |= os.ModeDir
	case syscall.S_IFIFO:
		m |= os.ModeNamedPipe
	case syscall.S_IFLNK:
		m |= os.ModeSymlink
	case syscall.S_IFSOCK:
		m |= os.ModeSocket
	}
	if f.st.Mode&syscall.S_ISGID != 0 {
		m |= os.ModeSetgid
	}
	if f.st.Mode&syscall.S_ISUID != 0 {
		m |= os.ModeSetuid
	}
	if f.st.Mode&syscall.S_ISVTX != 0 {
		m |= os.ModeSticky
	}
	return m
}

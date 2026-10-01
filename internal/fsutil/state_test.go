package fsutil

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mustWrite(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func lstamp(t *testing.T, p string) Stamp {
	t.Helper()
	st, err := LstatStamp(p)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestReadStateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "conf")
	mustWrite(t, p, "a=1\n", 0o640)
	s, err := ReadState(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != "file" || string(s.Data) != "a=1\n" || s.Target != "" || !s.Stable {
		t.Fatalf("state %+v", s)
	}
	if s.Meta.Mode != 0o640 || s.Meta.UID != os.Getuid() || s.Meta.GID != os.Getgid() {
		t.Fatalf("meta %+v", s.Meta)
	}
	if s.Meta.Stamp != lstamp(t, p) {
		t.Fatalf("stamp %+v, lstat %+v", s.Meta.Stamp, lstamp(t, p))
	}
}

// Symlinks are read as links, whatever they point to, and never followed.
func TestReadStateLinks(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "secret"), "do not read\n", 0o600)
	for name, target := range map[string]string{
		"dangling": "/nonexistent/x",
		"devnull":  "/dev/null",
		"todir":    dir,
		"tofile":   "secret",
		"long":     strings.Repeat("x/", 300) + "end",
	} {
		p := filepath.Join(dir, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		s, err := ReadState(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Kind != "link" || s.Target != target || s.Data != nil || !s.Stable {
			t.Errorf("%s: state %+v", name, s)
		}
		if s.Meta.Mode&os.ModeSymlink == 0 || s.Meta.Stamp != lstamp(t, p) {
			t.Errorf("%s: meta %+v, lstat %+v", name, s.Meta, lstamp(t, p))
		}
	}
}

func TestReadStateAbsent(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadState(filepath.Join(dir, "none")); !IsNotExist(err) {
		t.Fatalf("absent file: %v", err)
	}
	if _, err := ReadState(filepath.Join(dir, "nodir", "x")); !IsNotExist(err) {
		t.Fatalf("absent directory: %v", err)
	}
}

// Directories, FIFOs, sockets, devices and big files are refused, and a
// FIFO never blocks.
func TestReadStateNotRecordable(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, make([]byte, MaxSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	for p, msg := range map[string]string{
		dir:         "not a regular file",
		fifo:        "not a regular file",
		sock:        "not a regular file",
		"/dev/null": "not a regular file",
		big:         "8 MB",
	} {
		done := make(chan error, 1)
		go func() { _, err := ReadState(p); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotRecordable) || !strings.Contains(err.Error(), msg) {
				t.Errorf("%s: %v", p, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: blocked", p)
		}
	}
}

// The file's own directory must not be a symlink; a loop is reported as a
// loop.
func TestReadStateSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.Mkdir(real, 0o755)
	mustWrite(t, filepath.Join(real, "conf"), "x\n", 0o644)
	os.Symlink(real, filepath.Join(dir, "link"))
	_, err := ReadState(filepath.Join(dir, "link", "conf"))
	if err == nil || !strings.Contains(err.Error(), "is a symlink, refusing") || IsNotExist(err) {
		t.Fatalf("symlinked parent: %v", err)
	}
	if _, err := ReadState(filepath.Join(real, "conf")); err != nil {
		t.Fatalf("real parent: %v", err)
	}
}

// A change during the read clears Stable; the content read is still
// returned with the stamp from before the read.
func TestReadStateUnstable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conf")
	mustWrite(t, p, "one\n", 0o644)
	before := lstamp(t, p)
	testHookAfterRead = func() { mustWrite(t, p, "two, longer\n", 0o644) }
	defer func() { testHookAfterRead = nil }()
	s, err := ReadState(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Stable || string(s.Data) != "one\n" || s.Meta.Stamp != before {
		t.Fatalf("file changed while read: %+v", s)
	}

	// A rename over the path, and a chmod, during the read.
	for _, change := range []func(){
		func() { mustWrite(t, p+".new", "one\n", 0o644); os.Rename(p+".new", p) },
		func() { os.Chmod(p, 0o600) },
	} {
		mustWrite(t, p, "one\n", 0o644)
		testHookAfterRead = change
		if s, err := ReadState(p); err != nil || s.Stable {
			t.Fatalf("changed while read: %+v %v", s, err)
		}
	}

	l := filepath.Join(dir, "l")
	os.Symlink("a", l)
	testHookAfterRead = func() { os.Remove(l); os.Symlink("b", l) }
	if s, err := ReadState(l); err != nil || s.Stable || s.Target != "a" {
		t.Fatalf("link changed while read: %+v %v", s, err)
	}
}

// The stamp changes on chmod, a rename over the path and a write, and not
// on a read.
func TestStampChanges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conf")
	mustWrite(t, p, "x\n", 0o644)
	st := lstamp(t, p)
	if _, err := ReadState(p); err != nil {
		t.Fatal(err)
	}
	os.ReadFile(p)
	if lstamp(t, p) != st {
		t.Fatal("stamp changed on a read")
	}
	for name, change := range map[string]func(){
		"chmod":  func() { os.Chmod(p, 0o600) },
		"rename": func() { mustWrite(t, p+".new", "x\n", 0o600); os.Rename(p+".new", p) },
		"write":  func() { os.WriteFile(p, []byte("y\n"), 0o600) },
	} {
		before := lstamp(t, p)
		time.Sleep(10 * time.Millisecond) // ctime granularity
		change()
		if lstamp(t, p) == before {
			t.Errorf("stamp unchanged after %s", name)
		}
	}
}

// statFileInfo converts modes exactly as the os package does, so stamps
// from fstatat and from os.Lstat agree.
func TestStatFileInfoMode(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "reg")
	mustWrite(t, reg, "x", 0o644)
	os.Chmod(reg, 0o7755|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
	fifo := filepath.Join(dir, "fifo")
	syscall.Mkfifo(fifo, 0o600)
	link := filepath.Join(dir, "link")
	os.Symlink("reg", link)
	sock := filepath.Join(dir, "sock")
	l, _ := net.Listen("unix", sock)
	defer l.Close()
	for _, p := range []string{reg, dir, fifo, link, sock, "/dev/null"} {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(p, &st); err != nil {
			t.Fatal(err)
		}
		mine, _ := statInfo(filepath.Base(p), &st)
		if mine.Mode() != fi.Mode() || stampOf(mine) != stampOf(fi) {
			t.Errorf("%s: mode %v, os %v", p, mine.Mode(), fi.Mode())
		}
	}
}

// names lists a directory, sorted.
func names(t *testing.T, dir string) string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	sort.Strings(n)
	return strings.Join(n, " ")
}

func TestSymlinkAtomic(t *testing.T) {
	dir := t.TempDir()
	file, link, fresh := filepath.Join(dir, "file"), filepath.Join(dir, "link"), filepath.Join(dir, "fresh")
	mustWrite(t, file, "x\n", 0o644)
	os.Symlink("old", link)
	uid, gid := os.Getuid(), os.Getgid()
	for _, p := range []string{file, link, fresh} {
		if err := SymlinkAtomic(p, "/lib/systemd/system/x.service", uid, gid); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if got, err := os.Readlink(p); err != nil || got != "/lib/systemd/system/x.service" {
			t.Fatalf("%s: %q %v", p, got, err)
		}
	}
	if got := names(t, dir); got != "file fresh link" {
		t.Fatalf("directory holds %q", got)
	}

	// A failed rename (over a directory) leaves no temp link.
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	if err := SymlinkAtomic(sub, "x", uid, gid); err == nil {
		t.Fatal("replaced a directory")
	}
	if got := names(t, dir); got != "file fresh link sub" {
		t.Fatalf("directory holds %q", got)
	}

	// The parent must not be a symlink.
	os.Symlink(sub, filepath.Join(dir, "sublink"))
	if err := SymlinkAtomic(filepath.Join(dir, "sublink", "x"), "y", uid, gid); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("symlinked parent: %v", err)
	}
	if got := names(t, sub); got != "" {
		t.Fatalf("sub holds %q", got)
	}
}

// Root half: the temp link has its owner before it is renamed into place.
func TestSymlinkAtomicOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "unit")
	var tmpOwner [2]int
	testHookBeforeSymlinkRename = func(tmp string) {
		var st syscall.Stat_t
		if err := syscall.Lstat(tmp, &st); err != nil {
			t.Error(err)
		}
		tmpOwner = [2]int{int(st.Uid), int(st.Gid)}
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Error("the link is in place before the rename")
		}
	}
	defer func() { testHookBeforeSymlinkRename = nil }()
	if err := SymlinkAtomic(p, "/dev/null", 1234, 5678); err != nil {
		t.Fatal(err)
	}
	if tmpOwner != [2]int{1234, 5678} {
		t.Fatalf("temp link owned by %v before the rename", tmpOwner)
	}
	s, err := ReadState(p)
	if err != nil || s.Meta.UID != 1234 || s.Meta.GID != 5678 {
		t.Fatalf("%+v %v", s.Meta, err)
	}
}

func TestRemoveFile(t *testing.T) {
	dir := t.TempDir()
	file, link := filepath.Join(dir, "file"), filepath.Join(dir, "link")
	mustWrite(t, file, "x\n", 0o644)
	os.Symlink(file, link)
	if err := RemoveFile(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("removing the link removed its target")
	}
	if err := RemoveFile(file); err != nil {
		t.Fatal(err)
	}
	if got := names(t, dir); got != "" {
		t.Fatalf("directory holds %q", got)
	}
	if err := RemoveFile(file); !IsNotExist(err) {
		t.Fatalf("absent: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	if err := RemoveFile(sub); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory: %v", err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatal("directory removed")
	}
}

// Whatever replaces the path between fstatat and the open or readlink is
// ErrReplaced (so callers read again), never read and never followed.
func TestReadStateSwapBeforeOpen(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conf")
	other := filepath.Join(dir, "other")
	mustWrite(t, other, "secret\n", 0o600)
	defer func() { testHookBeforeOpen = nil }()
	for name, c := range map[string]struct {
		setup, swap func()
	}{
		"file for file": {func() { mustWrite(t, p, "x\n", 0o644) },
			func() { mustWrite(t, p+".n", "y\n", 0o644); os.Rename(p+".n", p) }},
		"file for link": {func() { mustWrite(t, p, "x\n", 0o644) },
			func() { os.Remove(p); os.Symlink(other, p) }},
		"file for fifo": {func() { mustWrite(t, p, "x\n", 0o644) },
			func() { os.Remove(p); syscall.Mkfifo(p, 0o644) }},
		"link for file": {func() { os.Symlink(other, p) },
			func() { os.Remove(p); mustWrite(t, p, "x\n", 0o644) }},
	} {
		os.Remove(p)
		c.setup()
		testHookBeforeOpen = c.swap
		done := make(chan error, 1)
		go func() { _, err := ReadState(p); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrReplaced) {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: blocked", name)
		}
	}
}

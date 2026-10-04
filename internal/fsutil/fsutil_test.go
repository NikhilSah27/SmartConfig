package fsutil

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func noTemps(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".sc-tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReadWithMeta(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, m, err := ReadWithMeta(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" || m.Mode.Perm() != 0o600 {
		t.Fatalf("got %q mode %v", data, m.Mode)
	}
	if m.UID != os.Getuid() || m.GID != os.Getgid() {
		t.Fatalf("got owner %d:%d", m.UID, m.GID)
	}
}

func TestReadWithMetaRefuses(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("x"), 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadWithMeta(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink: got %v", err)
	}
	if _, _, err := ReadWithMeta(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("dir: got %v", err)
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, make([]byte, MaxSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadWithMeta(big); err == nil || !strings.Contains(err.Error(), "8 MB") {
		t.Errorf("big: got %v", err)
	}
	if _, _, err := ReadWithMeta(filepath.Join(dir, "missing")); !IsNotExist(err) {
		t.Errorf("missing: got %v", err)
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("old"), 0o644)
	if err := WriteAtomic(p, []byte("new\n"), 0o600, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	data, m, err := ReadWithMeta(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("new\n")) || m.Mode.Perm() != 0o600 {
		t.Fatalf("got %q mode %v", data, m.Mode)
	}
	noTemps(t, dir)
}

func TestWriteAtomicFailureLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	// Renaming a file over a non-empty directory fails after the temp file
	// has been fully written, exercising the cleanup path.
	target := filepath.Join(dir, "sub")
	os.Mkdir(target, 0o755)
	os.WriteFile(filepath.Join(target, "x"), nil, 0o644)
	if err := WriteAtomic(target, []byte("data"), 0o644, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("expected error")
	}
	noTemps(t, dir)

	// A chown to another user fails for non-root.
	if os.Geteuid() != 0 {
		p := filepath.Join(dir, "f")
		os.WriteFile(p, []byte("keep"), 0o644)
		if err := WriteAtomic(p, []byte("data"), 0o644, 0, 0); err == nil {
			t.Fatal("expected chown error")
		}
		noTemps(t, dir)
		if b, _ := os.ReadFile(p); string(b) != "keep" {
			t.Fatalf("original changed: %q", b)
		}
	}
}

// The path is checked with lstat and then opened again. Anything swapped in
// between must be refused, never read.
func TestReadCheckedRefusesSwap(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig")
	other := filepath.Join(dir, "other")
	os.WriteFile(orig, []byte("config\n"), 0o644)
	os.WriteFile(other, []byte("secret\n"), 0o600)
	want, err := os.Lstat(orig)
	if err != nil {
		t.Fatal(err)
	}

	// Another regular file renamed over the path.
	if _, _, err := readChecked(other, want); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("different file: got %v", err)
	}

	// A symlink to a file root should not copy into the store.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readChecked(link, want); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink: got %v", err)
	}

	// A FIFO must not block the read.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := readChecked(fifo, want); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("fifo: read succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fifo: read blocked")
	}

	// The unchanged file still reads fine.
	if data, _, err := readChecked(orig, want); err != nil || string(data) != "config\n" {
		t.Errorf("unchanged file: %q %v", data, err)
	}
}

func TestPrepareCommitDiscard(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conf")
	os.WriteFile(p, []byte("old\n"), 0o644)

	// Prepared but discarded: target untouched, no temp file left.
	pend, err := PrepareAtomic(p, []byte("new\n"), 0o600, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "old\n" {
		t.Fatalf("prepare changed the target: %q", b)
	}
	pend.Discard()
	pend.Discard()
	noTemps(t, dir)

	// Prepared and committed: target replaced with content and mode.
	pend, err = PrepareAtomic(p, []byte("new\n"), 0o600, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	if err := pend.Commit(); err != nil {
		t.Fatal(err)
	}
	pend.Discard() // must not remove anything after a commit
	data, m, err := ReadWithMeta(p)
	if err != nil || string(data) != "new\n" || m.Mode.Perm() != 0o600 {
		t.Fatalf("after commit: %q %v %v", data, m.Mode, err)
	}
	noTemps(t, dir)
}

// The directory sync after a rename must not block if a FIFO has been put
// where the directory was.
func TestSyncDirDoesNotBlockOnFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- syncDir(fifo) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("syncDir accepted a FIFO")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("syncDir blocked on a FIFO")
	}
}

// A symlink loop in a directory component is not "a symlink": the real error
// must be reported.
func TestReadCheckedLoopInDirectory(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig")
	os.WriteFile(orig, []byte("x\n"), 0o644)
	want, _ := os.Lstat(orig)
	os.Symlink(filepath.Join(dir, "loop2"), filepath.Join(dir, "loop1"))
	os.Symlink(filepath.Join(dir, "loop1"), filepath.Join(dir, "loop2"))
	_, _, err := readChecked(filepath.Join(dir, "loop1", "file"), want)
	if err == nil || strings.Contains(err.Error(), "is a symlink") || !strings.Contains(err.Error(), "too many levels of symbolic links") {
		t.Fatalf("got %v", err)
	}
}

// RemovePending removes prepared temp files that were neither committed nor
// discarded, and never touches a committed target.
func TestRemovePending(t *testing.T) {
	// RemovePending closes the process-wide registry (the process is meant
	// to exit); reopen it for the other tests.
	t.Cleanup(func() {
		pendingTemps.Lock()
		pendingTemps.closed = false
		pendingTemps.Unlock()
	})
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	os.WriteFile(a, []byte("old a\n"), 0o644)
	os.WriteFile(b, []byte("old b\n"), 0o644)
	pa, err := PrepareAtomic(a, []byte("new a\n"), 0o644, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	if err := pa.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareAtomic(b, []byte("new b\n"), 0o644, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	RemovePending()
	noTemps(t, dir)
	// Once RemovePending has run, nothing new may be left behind.
	if _, err := PrepareAtomic(b, []byte("late\n"), 0o644, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("PrepareAtomic succeeded after RemovePending")
	}
	noTemps(t, dir)
	if got, _ := os.ReadFile(a); string(got) != "new a\n" {
		t.Fatalf("committed file touched: %q", got)
	}
	if got, _ := os.ReadFile(b); string(got) != "old b\n" {
		t.Fatalf("uncommitted target changed: %q", got)
	}
}

// syncDir deliberately follows a symlinked directory: the fsync has to reach
// the real directory that holds the renamed file.
func TestSyncDirFollowsSymlinkedDirectory(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	os.Mkdir(real, 0o755)
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := syncDir(link); err != nil {
		t.Fatalf("syncDir through a symlinked directory: %v", err)
	}
	p := filepath.Join(link, "conf")
	if err := WriteAtomic(p, []byte("x\n"), 0o644, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(real, "conf")); string(b) != "x\n" {
		t.Fatalf("content: %q", b)
	}
}

func TestReplacedIsErrReplaced(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(a, []byte("a"), 0o644)
	os.WriteFile(b, []byte("b"), 0o644)
	want, _ := os.Lstat(a)
	if _, _, err := readChecked(b, want); !errors.Is(err, ErrReplaced) {
		t.Fatalf("got %v", err)
	}
}

// Chown before chmod: the setuid bit survives an atomic write.
func TestWriteAtomicKeepsSetuid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tool")
	mode := os.FileMode(0o755) | os.ModeSetuid
	if err := WriteAtomic(p, []byte("x"), mode, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode() != mode {
		t.Fatalf("mode %v, want %v", fi.Mode(), mode)
	}
}

// PrivateDir makes a 0700 directory in the first parent it can, names
// every place tried when none works, removes this user's leftovers older
// than an hour (never fresh ones, never others' names), and the
// directory is removed by RemoveDir or, when a signal ends the process,
// by RemovePending.
func TestPrivateDir(t *testing.T) {
	t.Cleanup(func() {
		pendingTemps.Lock()
		pendingTemps.closed = false
		pendingTemps.Unlock()
	})
	if os.Geteuid() != 0 {
		ro := t.TempDir()
		os.Chmod(ro, 0o500)
		t.Cleanup(func() { os.Chmod(ro, 0o700) })
		parent := t.TempDir()
		dir, err := PrivateDir("sc-x-", []string{ro, filepath.Join(t.TempDir(), "missing"), parent})
		if err != nil || filepath.Dir(dir) != parent {
			t.Fatalf("%q %v", dir, err)
		}
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Errorf("mode %v", fi.Mode())
		}
		RemoveDir(dir)
		if _, err := PrivateDir("sc-x-", []string{ro}); err == nil || !strings.Contains(err.Error(), ro+" (permission denied)") {
			t.Errorf("none writable: %v", err)
		}
	}

	parent := t.TempDir()
	stale, fresh, other := filepath.Join(parent, "sc-x-old"), filepath.Join(parent, "sc-x-new"), filepath.Join(parent, "sc-y-old")
	for _, d := range []string{stale, fresh, other} {
		os.MkdirAll(filepath.Join(d, "sub"), 0o700)
	}
	long := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stale, long, long)
	os.Chtimes(other, long, long)
	dir, err := PrivateDir("sc-x-", []string{parent})
	if err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]bool{stale: false, fresh: true, other: true, dir: true} {
		if _, err := os.Stat(d); (err == nil) != want {
			t.Errorf("%s: exists %v, want %v", filepath.Base(d), err == nil, want)
		}
	}
	RemovePending() // a signal ends the process
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("not removed by RemovePending: %v", err)
	}
}

// Root's private directories go to a tmpfs first, a user's to their own
// runtime directory first (and never to one that is not theirs).
func TestTempParents(t *testing.T) {
	p := TempParents()
	if os.Geteuid() == 0 {
		if p[0] != "/run" || p[1] != "/dev/shm" {
			t.Errorf("root: %q", p)
		}
		return
	}
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	if p = TempParents(); p[0] != xdg || p[len(p)-1] != "/dev/shm" {
		t.Errorf("user: %q", p)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/") // root's
	if p = TempParents(); p[0] == "/" {
		t.Errorf("another user's runtime directory: %q", p)
	}
}

package fsutil

import (
	"bytes"
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

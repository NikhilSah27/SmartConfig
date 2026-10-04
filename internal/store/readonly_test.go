package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readOnly makes the store in home one sc may not write, as a read-only
// root does for root (access(2) says no either way), and undoes it at the
// end of the test. Root writes through modes: such tests skip.
func readOnly(t *testing.T, home string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes through file modes")
	}
	os.Chmod(filepath.Join(home, "changes.db"), 0o400)
	os.Chmod(home, 0o500)
	t.Cleanup(func() {
		os.Chmod(home, 0o700)
		os.Chmod(filepath.Join(home, "changes.db"), 0o600)
	})
}

// sums is the sha256 of each file directly in dir, by name.
func sums(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			out[e.Name()] = fmt.Sprintf("%x", sha256.Sum256(b))
		}
	}
	return out
}

// A store sc may not write (the rescue shell's read-only root) is read
// with mode=ro: rows and contents come back, every write says so, and no
// file of the store changes.
func TestReadOnlyStore(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(p, []byte("127.0.0.1 localhost\n"), 0o644)
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := s.Snapshot(p, OriginManual, "before")
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	readOnly(t, home)
	before := sums(t, home)

	s, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Copied() {
		t.Error("a store without a hot journal was copied")
	}
	if cs, err := s.List(p, 0); err != nil || len(cs) != 1 || cs[0].ID != c.ID {
		t.Fatalf("list %v %v", cs, err)
	}
	if b, err := s.Blob(c.Blob); err != nil || string(b) != "127.0.0.1 localhost\n" {
		t.Fatalf("blob %q %v", b, err)
	}
	os.WriteFile(p, []byte("changed\n"), 0o644)
	if _, _, err := s.Snapshot(p, OriginManual, "after"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("snapshot: %v", err)
	}
	if _, err := s.Record([]Obs{{Path: p, Origin: OriginAuto}}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("record: %v", err)
	}
	if after := sums(t, home); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the store changed:\n%v\n%v", before, after)
	}
}

// A hot journal on a store sc may not write: the store is read from a
// repaired copy (its committed rows), no file of the store changes, the
// copy is gone after Close, and the next writable open repairs the
// store itself.
func TestReadOnlyHotJournal(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	leaveHotJournal(t, home)
	readOnly(t, home)
	parent := t.TempDir()
	old := copyParent
	copyParent = func() string { return parent }
	t.Cleanup(func() { copyParent = old })
	before := sums(t, home)
	if _, ok := before["changes.db-journal"]; !ok {
		t.Fatal("no journal")
	}

	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Copied() {
		t.Error("not read from a copy")
	}
	if cs, err := s.List("", 0); err != nil || len(cs) != 5 {
		t.Fatalf("%d rows, want the 5 committed: %v", len(cs), err)
	}
	if _, _, err := s.Snapshot(filepath.Join(home, "changes.db"), OriginManual, ""); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a write to the copy: %v", err)
	}
	s.Close()
	if left, _ := os.ReadDir(parent); len(left) != 0 {
		t.Errorf("copy left: %v", left)
	}
	if after := sums(t, home); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the store changed:\n%v\n%v", before, after)
	}

	// Writable again: the store itself is rolled back.
	os.Chmod(home, 0o700)
	os.Chmod(filepath.Join(home, "changes.db"), 0o600)
	s, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Copied() {
		t.Error("a writable store was copied")
	}
	if cs, err := s.List("", 0); err != nil || len(cs) != 5 {
		t.Fatalf("%d rows after repair: %v", len(cs), err)
	}
	if _, err := os.Stat(filepath.Join(home, "changes.db-journal")); !os.IsNotExist(err) {
		t.Errorf("journal left: %v", err)
	}
}

// A store at an older schema cannot be upgraded where sc may not write
// it: Open says so, and nothing changes.
func TestReadOnlyOldSchema(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec("PRAGMA user_version = 0")
	s.Close()
	readOnly(t, home)
	before := sums(t, home)
	if _, err := Open(home); !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "remount") {
		t.Errorf("open: %v", err)
	}
	if after := sums(t, home); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the store changed")
	}
}

package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// readOnly makes the store in home one sc may not write, as a read-only
// root does for root (access(2) says no either way), and undoes it at the
// end of the test. Root writes through modes: such tests skip, and
// TestReadOnlyMount covers root.
func readOnly(t *testing.T, home string, dirMode, dbMode os.FileMode) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes through file modes (TestReadOnlyMount covers root)")
	}
	os.Chmod(filepath.Join(home, "changes.db"), dbMode)
	os.Chmod(home, dirMode)
	t.Cleanup(func() {
		os.Chmod(home, 0o700)
		os.Chmod(filepath.Join(home, "changes.db"), 0o600)
	})
}

// sums is the sha256 of every file under dir, objects included, by path.
func sums(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			out = append(out, fmt.Sprintf("%s %x", strings.TrimPrefix(p, dir), sha256.Sum256(b)))
		}
		return nil
	})
	return strings.Join(out, "\n")
}

// privateCopies points the repaired copies at a directory of the test and
// returns it.
func privateCopies(t *testing.T) string {
	parent := t.TempDir()
	old := copyParents
	copyParents = func() []string { return []string{parent} }
	t.Cleanup(func() { copyParents = old })
	return parent
}

// storeWithRow makes a store in a new home with one snapshot of a file and
// returns the home, the file and the row.
func storeWithRow(t *testing.T) (home, file string, c Change) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(file, []byte("127.0.0.1 localhost\n"), 0o644)
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, _, err = s.Snapshot(file, OriginManual, "before")
	if err != nil {
		t.Fatal(err)
	}
	return home, file, c
}

// A store sc may not write (the rescue shell's read-only root) is read
// with mode=ro: rows and contents come back, every write says why it
// cannot be made, and no file of the store changes, objects included.
func TestReadOnlyStore(t *testing.T) {
	for _, tc := range []struct {
		name          string
		dirMode, mode os.FileMode
	}{
		{"read-only directory", 0o500, 0o400},
		{"read-only database file", 0o700, 0o400}, // access(file) decides
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, file, c := storeWithRow(t)
			readOnly(t, home, tc.dirMode, tc.mode)
			before := sums(t, home)
			s, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.Copied() {
				t.Error("a store without a hot journal was copied")
			}
			if cs, err := s.List(file, 0); err != nil || len(cs) != 1 || cs[0].ID != c.ID {
				t.Fatalf("list %v %v", cs, err)
			}
			if b, err := s.Blob(c.Blob); err != nil || string(b) != "127.0.0.1 localhost\n" {
				t.Fatalf("blob %q %v", b, err)
			}
			os.WriteFile(file, []byte("changed\n"), 0o644)
			_, _, err = s.Snapshot(file, OriginManual, "after")
			if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), home+": the store can only be read here: this user may not write it") {
				t.Errorf("snapshot: %v", err)
			}
			if _, err := s.Record([]Obs{{Path: file, Origin: OriginAuto}}); !errors.Is(err, ErrReadOnly) {
				t.Errorf("record: %v", err)
			}
			if _, _, err := s.Restore(c.ID); !errors.Is(err, ErrReadOnly) {
				t.Errorf("restore: %v", err)
			}
			if err := s.CanReplace(file, nil); !errors.Is(err, ErrReadOnly) {
				t.Errorf("can replace: %v", err)
			}
			if after := sums(t, home); after != before {
				t.Errorf("the store changed:\n%s\n%s", before, after)
			}
		})
	}
}

// A hot journal on a store sc may not write: the store is read from a
// repaired copy (its committed rows), no file of the store changes, the
// copy is gone after Close, and the next writable open repairs the
// store itself. A writable database file in a read-only directory is not
// rolled back in place either (mode=ro).
func TestReadOnlyHotJournal(t *testing.T) {
	for _, dbMode := range []os.FileMode{0o400, 0o600} {
		t.Run(fmt.Sprintf("database %04o", dbMode), func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			if err := Init(home); err != nil {
				t.Fatal(err)
			}
			leaveHotJournal(t, home)
			readOnly(t, home, 0o500, dbMode)
			parent := privateCopies(t)
			before := sums(t, home)
			if !strings.Contains(before, "/changes.db-journal ") {
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
			_, _, err = s.Snapshot(filepath.Join(home, "changes.db"), OriginManual, "")
			if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "store "+home+": ") || !strings.Contains(err.Error(), "a repaired copy") {
				t.Errorf("a write to the copy: %v", err)
			}
			s.Close()
			if left, _ := os.ReadDir(parent); len(left) != 0 {
				t.Errorf("copy left: %v", left)
			}
			if after := sums(t, home); after != before {
				t.Errorf("the store changed:\n%s\n%s", before, after)
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
		})
	}
}

// A journal rolled back by another sc between the failed open and the
// copy: the store is read again, not refused.
func TestReadOnlyJournalGone(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	leaveHotJournal(t, home)
	readOnly(t, home, 0o500, 0o400)
	parent := t.TempDir()
	old := copyParents
	t.Cleanup(func() { copyParents = old })
	copyParents = func() []string {
		// Another sc, which may write, repairs the store first.
		os.Chmod(home, 0o700)
		os.Chmod(filepath.Join(home, "changes.db"), 0o600)
		if s, err := Open(home); err == nil {
			s.Close()
		}
		return []string{parent}
	}
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Copied() {
		t.Error("read from a copy")
	}
	if cs, err := s.List("", 0); err != nil || len(cs) != 5 {
		t.Fatalf("%d rows: %v", len(cs), err)
	}
}

// A copy is read only: a store at an older schema is not migrated there,
// a newer one is refused, and the messages name the store, not the copy.
func TestReadOnlyCopySchema(t *testing.T) {
	for _, tc := range []struct {
		version int
		want    string
	}{
		{0, ": the store can only be read here: it has a write a crash left unfinished"},
		{9, " was written by a newer sc (schema 9)"},
	} {
		home := filepath.Join(t.TempDir(), "home")
		if err := Init(home); err != nil {
			t.Fatal(err)
		}
		s, err := Open(home)
		if err != nil {
			t.Fatal(err)
		}
		s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", tc.version))
		s.Close()
		os.WriteFile(filepath.Join(home, "changes.db-journal"), nil, 0o600) // copied with the database
		parent := privateCopies(t)
		_, err = openCopy(home, "a reason")
		if err == nil || !strings.Contains(err.Error(), "store "+home+tc.want) {
			t.Errorf("schema %d: %v", tc.version, err)
		}
		if left, _ := os.ReadDir(parent); len(left) != 0 {
			t.Errorf("schema %d: copy left: %v", tc.version, left)
		}
	}
}

// A store at an older schema cannot be upgraded where sc may not write
// it: Open says so, and nothing changes.
func TestReadOnlyOldSchema(t *testing.T) {
	home, _, _ := storeWithRow(t)
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec("PRAGMA user_version = 0")
	s.Close()
	readOnly(t, home, 0o500, 0o400)
	before := sums(t, home)
	if _, err := Open(home); !errors.Is(err, ErrReadOnly) {
		t.Errorf("open: %v", err)
	}
	if after := sums(t, home); after != before {
		t.Errorf("the store changed")
	}
}

// As root on a read-only mount (the rescue shell itself): run in a
// private mount namespace (unshare), the store is bind-mounted read-only;
// it is read, from a repaired copy when a crash left a journal, writes
// say to remount, and nothing changes.
func TestReadOnlyMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root for a read-only mount")
	}
	if os.Getenv("SC_TEST_MOUNT_NS") == "" {
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			t.Skip("no unshare")
		}
		cmd := exec.Command(unshare, "--mount", "--propagation", "private", os.Args[0], "-test.run=^TestReadOnlyMount$", "-test.count=1")
		cmd.Env = append(os.Environ(), "SC_TEST_MOUNT_NS=1")
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "PASS") {
			t.Fatalf("in a mount namespace: %v\n%s", err, out)
		}
		return
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	leaveHotJournal(t, home)
	if err := syscall.Mount(home, home, "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount("", home, "", syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
		t.Fatal(err)
	}
	defer syscall.Unmount(home, 0)
	parent := privateCopies(t)
	before := sums(t, home)
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Copied() {
		t.Error("not read from a copy")
	}
	if cs, err := s.List("", 0); err != nil || len(cs) != 5 {
		t.Fatalf("%d rows: %v", len(cs), err)
	}
	if _, _, err := s.Snapshot(filepath.Join(home, "changes.db"), OriginManual, ""); !errors.Is(err, ErrReadOnly) ||
		!strings.Contains(err.Error(), "it is on a read-only file system: remount it read-write first") {
		t.Errorf("a write: %v", err)
	}
	s.Close()
	if left, _ := os.ReadDir(parent); len(left) != 0 {
		t.Errorf("copy left: %v", left)
	}
	if after := sums(t, home); after != before {
		t.Errorf("the store changed")
	}
}

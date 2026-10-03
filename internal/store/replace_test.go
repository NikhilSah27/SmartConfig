package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smartconfig/internal/fsutil"
)

// base reads p as sc edit does before the editor runs.
func base(t *testing.T, p string) *fsutil.State {
	t.Helper()
	st, err := fsutil.ReadState(p)
	if err != nil {
		t.Fatal(err)
	}
	return &st
}

func replace(t *testing.T, s *Store, p, data string, mode os.FileMode, b *fsutil.State) (Change, *Change) {
	t.Helper()
	row, prev, err := s.Replace(p, []byte(data), mode, os.Getuid(), os.Getgid(), b, OriginEdit, "sc edit")
	if err != nil {
		t.Fatal(err)
	}
	return row, prev
}

func TestReplace(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "one\n", 0o640)

	// A file with no rows yet: the state before is saved, then the edit.
	row, prev := replace(t, s, p, "two\n", 0o640, base(t, p))
	wantHistory(t, s, p, "file before sc edit", "file sc edit")
	if b, _ := os.ReadFile(p); string(b) != "two\n" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if row.Origin != OriginEdit || row.Size != 4 || prev == nil || prev.Origin != OriginManual {
		t.Fatalf("row %+v prev %+v", row, prev)
	}
	if got, _ := s.Blob(prev.Blob); string(got) != "one\n" {
		t.Fatalf("the state before holds %q", got)
	}
	if got, _ := s.Blob(row.Blob); string(got) != "two\n" {
		t.Fatalf("the edit row holds %q", got)
	}

	// The newest row already holds the state before: only the edit row.
	row2, prev2 := replace(t, s, p, "three\n", 0o600, base(t, p))
	wantHistory(t, s, p, "file before sc edit", "file sc edit", "file sc edit")
	if prev2 == nil || prev2.ID != row.ID || row2.Mode.Perm() != 0o600 {
		t.Fatalf("prev %+v row %+v", prev2, row2)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// A snapshot or a watcher's look right after finds nothing new.
	if _, unchanged := snap(t, s, p); !unchanged {
		t.Fatal("a snapshot after the edit recorded a row")
	}
	if res := record(t, s, observe(t, p)); res[0].Recorded {
		t.Fatal("an observation after the edit recorded a row")
	}

	// The edit can be undone with the row before it.
	if _, _, err := s.Restore(prev2.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two\n" {
		t.Fatalf("after restore %q", b)
	}
}

// A new file: its path gets a "did not exist" row first, so the creation
// can be undone; a path that has rows gets none.
func TestReplaceNewFile(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "90-new")
	row, prev := replace(t, s, p, "new\n", 0o440, nil)
	wantHistory(t, s, p, "deleted did not exist", "file sc edit")
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o440 || prev != nil || row.Origin != OriginEdit {
		t.Fatalf("%v %v prev %+v row %+v", fi, err, prev, row)
	}
	cs, _ := s.List(p, 0)
	if cs[1].Origin != OriginEdit {
		t.Fatalf("did-not-exist row origin %q", cs[1].Origin)
	}
	if _, _, err := s.Restore(cs[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatal("restoring the did-not-exist row left the file")
	}
	// The newest row already says the path is absent: a second creation
	// adds only its edit row.
	n := len(history(t, s, p))
	replace(t, s, p, "again\n", 0o440, nil)
	if h := history(t, s, p); len(h) != n+1 || h[len(h)-1] != "file sc edit" {
		t.Fatalf("history %q", h)
	}
	// The file vanished with nobody recording it: the newest row is a
	// stale file row, so the creation first records that the path was
	// absent, and restoring that row undoes the creation.
	os.Remove(p)
	replace(t, s, p, "third\n", 0o440, nil)
	h := history(t, s, p)
	if len(h) != n+3 || h[len(h)-2] != "deleted deleted" || h[len(h)-1] != "file sc edit" {
		t.Fatalf("history after an unrecorded deletion %q", h)
	}
	cs, _ = s.List(p, 2)
	if _, _, err := s.Restore(cs[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatal("restoring the deleted row left the file")
	}
}

// The file is no longer the version the caller read: nothing is written.
func TestReplaceFileChanged(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "one\n", 0o644)
	b := base(t, p)
	write(t, p, "someone else's edit\n", 0o644)
	_, _, err := s.Replace(p, []byte("mine\n"), 0o644, os.Getuid(), os.Getgid(), b, OriginEdit, "sc edit")
	if !errors.Is(err, ErrFileChanged) || !strings.HasSuffix(err.Error(), "(file not changed)") {
		t.Fatalf("changed: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "someone else's edit\n" {
		t.Fatalf("content %q", got)
	}
	wantHistory(t, s, p)

	// Changed between the save of the state before and the write.
	b = base(t, p)
	testHookBeforeReplaceLock = func() { write(t, p, "raced\n", 0o644) }
	defer func() { testHookBeforeReplaceLock = nil }()
	_, prev, err := s.Replace(p, []byte("mine\n"), 0o644, os.Getuid(), os.Getgid(), b, OriginEdit, "sc edit")
	if !errors.Is(err, ErrFileChanged) || prev == nil {
		t.Fatalf("raced: %v, prev %+v", err, prev)
	}
	if got, _ := os.ReadFile(p); string(got) != "raced\n" {
		t.Fatalf("content %q", got)
	}
	wantHistory(t, s, p, "file before sc edit")
	testHookBeforeReplaceLock = nil

	// Gone meanwhile; and created meanwhile where the caller saw nothing.
	b = base(t, p)
	os.Remove(p)
	if _, _, err := s.Replace(p, []byte("mine\n"), 0o644, os.Getuid(), os.Getgid(), b, OriginEdit, "sc edit"); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("gone: %v", err)
	}
	write(t, p, "appeared\n", 0o644)
	if _, _, err := s.Replace(p, []byte("mine\n"), 0o644, os.Getuid(), os.Getgid(), nil, OriginEdit, "sc edit"); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("appeared: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "appeared\n" {
		t.Fatalf("content %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}

	// Content sc would refuse to read back is refused before it is written.
	b = base(t, p)
	_, _, err = s.Replace(p, make([]byte, fsutil.MaxSize+1), 0o644, os.Getuid(), os.Getgid(), b, OriginEdit, "sc edit")
	if err == nil || !strings.Contains(err.Error(), "is larger than the 8 MB limit (file not changed)") || content(t, p) != "appeared\n" {
		t.Fatalf("too big: %v", err)
	}
}

// Refusals come before anything is written.
func TestReplaceRefusals(t *testing.T) {
	s, dir := setup(t)
	args := func(p string, b *fsutil.State) error {
		_, _, err := s.Replace(p, []byte("x\n"), 0o644, os.Getuid(), os.Getgid(), b, OriginEdit, "sc edit")
		return err
	}
	key := filepath.Join(dir, "ssh_host_key")
	write(t, key, "PRIVATE\n", 0o600)
	s.SetFingerprintOnly(func(p string) bool { return p == key })
	if err := args(key, base(t, key)); err == nil || !strings.Contains(err.Error(), "is fingerprint-only; sc never writes it") {
		t.Fatalf("fingerprint-only: %v", err)
	}
	real := filepath.Join(dir, "real")
	os.Mkdir(real, 0o755)
	write(t, filepath.Join(real, "conf"), "x\n", 0o644)
	os.Symlink(real, filepath.Join(dir, "link"))
	via := filepath.Join(dir, "link", "conf")
	if err := args(via, base(t, filepath.Join(real, "conf"))); err == nil || err.Error() != "refusing to write into "+filepath.Join(dir, "link")+" (a symlink)" {
		t.Fatalf("symlinked directory: %v", err)
	}
	if err := args(filepath.Join(dir, "nodir", "conf"), nil); err == nil || !strings.Contains(err.Error(), "(file not changed)") {
		t.Fatalf("missing directory: %v", err)
	}
	ln := filepath.Join(dir, "ln")
	os.Symlink(key, ln)
	if err := args(ln, base(t, ln)); err == nil || !strings.Contains(err.Error(), "is a symlink; sc writes regular files only") {
		t.Fatalf("symlink: %v", err)
	}
	for _, p := range []string{key, via, ln} {
		if cs, _ := s.List(p, 0); len(cs) != 0 {
			t.Errorf("%s has rows: %+v", p, cs)
		}
	}
	if b, _ := os.ReadFile(key); string(b) != "PRIVATE\n" {
		t.Fatal("the fingerprint-only file was written")
	}
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/fsutil"
)

// setup points SC_HOME at a temp dir, initialises it and returns an open store
// plus a separate directory for the files being snapshotted.
func setup(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("SC_HOME", t.TempDir())
	if err := Init(Home()); err != nil {
		t.Fatal(err)
	}
	if err := Init(Home()); err != nil {
		t.Fatalf("second init: %v", err)
	}
	s, err := Open(Home())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, t.TempDir()
}

// clock makes the store's time controllable; tick advances it one second.
func clock(s *Store) (tick func()) {
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	return func() { now = now.Add(time.Second) }
}

func write(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func snap(t *testing.T, s *Store, p string) (Change, bool) {
	t.Helper()
	c, unchanged, err := s.Snapshot(p, OriginManual, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c, unchanged
}

func countBlobs(t *testing.T) int {
	t.Helper()
	n := 0
	filepath.WalkDir(filepath.Join(Home(), "objects"), func(_ string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestOpenUninitialised(t *testing.T) {
	_, err := Open(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "sc init") {
		t.Fatalf("got %v", err)
	}
}

func TestInitMode(t *testing.T) {
	setup(t)
	fi, err := os.Stat(Home())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestSnapshotUnchanged(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "hosts")
	write(t, p, "a\n", 0o644)
	c1, u1 := snap(t, s, p)
	c2, u2 := snap(t, s, p)
	if u1 || !u2 {
		t.Fatalf("unchanged flags: %v %v", u1, u2)
	}
	if c2.ID != c1.ID {
		t.Fatalf("unchanged should report %s, got %s", c1.ID, c2.ID)
	}
	cs, _ := s.List(p, 0)
	if len(cs) != 1 {
		t.Fatalf("want 1 row, got %d", len(cs))
	}
}

func TestSnapshotModifyLog(t *testing.T) {
	s, dir := setup(t)
	clock(s) // both snapshots in the same second: order must come from rowid
	p := filepath.Join(dir, "hosts")
	write(t, p, "a\n", 0o644)
	c1, _ := snap(t, s, p)
	write(t, p, "a\nb\n", 0o644)
	c2, _ := snap(t, s, p)
	cs, err := s.List(p, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].ID != c2.ID || cs[1].ID != c1.ID {
		t.Fatalf("want newest first [%s %s], got %+v", c2.ID, c1.ID, cs)
	}
	if n := countBlobs(t); n != 2 {
		t.Fatalf("want 2 blobs, got %d", n)
	}
	if cs[0].Size != 4 || cs[1].Size != 2 {
		t.Fatalf("sizes %d %d", cs[0].Size, cs[1].Size)
	}
	fi, _ := os.Stat(filepath.Join(Home(), "objects", c1.Blob[:2], c1.Blob))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("blob mode %v", fi.Mode())
	}
	if all, _ := s.List("", 1); len(all) != 1 {
		t.Fatalf("limit ignored: %d rows", len(all))
	}
}

func TestSnapshotRefuses(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "real")
	write(t, p, "x", 0o644)
	link := filepath.Join(dir, "link")
	os.Symlink(p, link)
	if _, _, err := s.Snapshot(link, OriginManual, ""); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestSnapshotRelativePath(t *testing.T) {
	s, dir := setup(t)
	write(t, filepath.Join(dir, "f"), "x", 0o644)
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	c, _ := snap(t, s, "f")
	if c.Path != filepath.Join(dir, "f") {
		t.Fatalf("path %q not absolute", c.Path)
	}
}

func TestRestore(t *testing.T) {
	s, dir := setup(t)
	tick := clock(s)
	p := filepath.Join(dir, "secret")
	write(t, p, "original\n", 0o600)
	orig, _ := snap(t, s, p)
	tick()
	write(t, p, "broken\n", 0o644)
	edited, _ := snap(t, s, p)
	tick()

	restored, prev, err := s.Restore(orig.ID[:4])
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("original\n")) {
		t.Fatalf("content %q", data)
	}
	fi, _ := os.Stat(p)
	if fi.Mode() != orig.Mode || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want %v", fi.Mode(), orig.Mode)
	}
	if prev == nil || prev.Origin != OriginPreRestore || prev.Blob != edited.Blob {
		t.Fatalf("prev %+v", prev)
	}
	if restored.Intent != "restored from "+orig.ID {
		t.Fatalf("intent %q", restored.Intent)
	}

	cs, _ := s.List(p, 0)
	var origins []string
	for _, c := range cs {
		origins = append(origins, c.Origin)
	}
	want := "restore pre-restore manual manual"
	if got := strings.Join(origins, " "); got != want {
		t.Fatalf("log origins %q, want %q", got, want)
	}
}

func TestRestoreOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("uid/gid restore needs root")
	}
	s, dir := setup(t)
	p := filepath.Join(dir, "owned")
	write(t, p, "x\n", 0o640)
	os.Chown(p, 1234, 5678)
	c, _ := snap(t, s, p)
	os.Chown(p, 0, 0)
	write(t, p, "y\n", 0o640)
	if _, _, err := s.Restore(c.ID); err != nil {
		t.Fatal(err)
	}
	_, m, err := fsutil.ReadWithMeta(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.UID != 1234 || m.GID != 5678 {
		t.Fatalf("owner %d:%d", m.UID, m.GID)
	}
}

// Same path, same second, same content: the spec formula collides, so the
// pre-restore row must get a different id instead of failing.
func TestRestoreSameSecondSameContent(t *testing.T) {
	s, dir := setup(t)
	clock(s)
	p := filepath.Join(dir, "hosts")
	write(t, p, "a\n", 0o644)
	c1, _ := snap(t, s, p)
	write(t, p, "b\n", 0o644)
	c2, _ := snap(t, s, p)
	restored, prev, err := s.Restore(c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Restoring again in the same second: pre-restore has c1's content.
	restored2, prev2, err := s.Restore(c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, id := range []string{c1.ID, c2.ID, prev.ID, restored.ID, prev2.ID, restored2.ID} {
		if len(id) != 6 || ids[id] {
			t.Fatalf("bad or duplicate id %q in %v", id, ids)
		}
		ids[id] = true
	}
}

func TestRestoreMissingFile(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "gone")
	write(t, p, "x\n", 0o644)
	c, _ := snap(t, s, p)
	os.Remove(p)
	_, prev, err := s.Restore(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev != nil {
		t.Fatalf("expected no pre-restore row, got %+v", prev)
	}
	if b, _ := os.ReadFile(p); string(b) != "x\n" {
		t.Fatalf("content %q", b)
	}
}

func TestDiff(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "hosts")
	write(t, p, "one\ntwo\nthree\n", 0o644)
	c1, _ := snap(t, s, p)
	write(t, p, "one\ntwo\nthree\nfour\n", 0o644)
	c2, _ := snap(t, s, p)
	a, _ := s.Blob(c1.Blob)
	b, _ := s.Blob(c2.Blob)
	got, err := UnifiedDiff(a, b, "a/x", "b/x")
	if err != nil {
		t.Fatal(err)
	}
	want := "--- a/x\n+++ b/x\n@@ -1,3 +1,4 @@\n one\n two\n three\n+four\n"
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
	if got, _ := UnifiedDiff(a, a, "a/x", "b/x"); got != "" {
		t.Fatalf("identical content gave %q", got)
	}
}

func TestDiffMissingNewline(t *testing.T) {
	got, _ := UnifiedDiff([]byte("a\n"), []byte("a"), "a", "b")
	want := "--- a\n+++ b\n@@ -1 +1 @@\n-a\n+a\n\\ No newline at end of file\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestGetPrefix(t *testing.T) {
	s, dir := setup(t)
	for i, id := range []string{"abc111", "abc222", "def333"} {
		c := Change{ID: id, TS: int64(i), Path: filepath.Join(dir, "f"), Blob: strings.Repeat("0", 64), Origin: OriginManual}
		if _, err := s.db.Exec(`INSERT INTO changes (id, ts, path, blob, size, mode, uid, gid, origin) VALUES (?,?,?,?,0,420,0,0,?)`,
			c.ID, c.TS, c.Path, c.Blob, c.Origin); err != nil {
			t.Fatal(err)
		}
	}
	if c, err := s.Get("def"); err != nil || c.ID != "def333" {
		t.Fatalf("unique prefix: %v %v", c.ID, err)
	}
	if c, err := s.Get("ABC1"); err != nil || c.ID != "abc111" {
		t.Fatalf("uppercase prefix: %v %v", c.ID, err)
	}
	_, err := s.Get("abc")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), "abc111") || !strings.Contains(err.Error(), "abc222") {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := s.Get("fff"); err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Fatalf("unknown: %v", err)
	}
	for _, bad := range []string{"", "xyz", "abc1112"} {
		if _, err := s.Get(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestBlobCorrupt(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "f")
	write(t, p, "x\n", 0o644)
	c, _ := snap(t, s, p)
	os.WriteFile(filepath.Join(Home(), "objects", c.Blob[:2], c.Blob), []byte("y\n"), 0o600)
	if _, err := s.Blob(c.Blob); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("got %v", err)
	}
	if _, _, err := s.Restore(c.ID); err == nil {
		t.Fatal("restored from a corrupt blob")
	}
	if b, _ := os.ReadFile(p); string(b) != "x\n" {
		t.Fatalf("file changed: %q", b)
	}
}

// A watcher that sees the restore's rename and then takes the write lock to
// record it must wait for the restore row, never record the change as its
// own. So when the file is in place the lock must still be held and the row
// not yet visible; after Restore returns the row must be there.
func TestRestoreHoldsLockAcrossRename(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "hosts")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "broken\n", 0o644)

	other, err := sql.Open("sqlite", "file:"+filepath.Join(Home(), "changes.db")+"?_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	restoreRows := func() int {
		var n int
		if err := other.QueryRow(`SELECT count(*) FROM changes WHERE origin = 'restore'`).Scan(&n); err != nil {
			t.Fatalf("read from second connection: %v", err)
		}
		return n
	}

	hookRan := false
	testHookAfterRestoreWrite = func() {
		hookRan = true
		if b, _ := os.ReadFile(p); string(b) != "good\n" {
			t.Errorf("file not yet restored inside the lock: %q", b)
		}
		if n := restoreRows(); n != 0 {
			t.Errorf("restore row visible before commit: %d", n)
		}
		conn, err := other.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err == nil {
			conn.ExecContext(context.Background(), "ROLLBACK")
			t.Error("a watcher could take the write lock while the restore is uncommitted")
		}
	}
	defer func() { testHookAfterRestoreWrite = nil }()

	if _, _, err := s.Restore(good.ID); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("hook did not run")
	}
	if n := restoreRows(); n != 1 {
		t.Fatalf("restore rows after Restore: %d, want 1", n)
	}
}

// If the file cannot be written, no restore row may be recorded.
func TestRestoreWriteFailureRecordsNothing(t *testing.T) {
	s, dir := setup(t)
	sub := filepath.Join(dir, "gone")
	os.Mkdir(sub, 0o755)
	p := filepath.Join(sub, "conf")
	write(t, p, "x\n", 0o644)
	c, _ := snap(t, s, p)
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Restore(c.ID)
	if err == nil || !strings.Contains(err.Error(), "restore "+p) {
		t.Fatalf("got %v", err)
	}
	cs, _ := s.List(p, 0)
	if len(cs) != 1 || cs[0].Origin != OriginManual {
		t.Fatalf("history changed by a failed restore: %+v", cs)
	}
}

// holdReadLock opens a second connection, starts a read transaction and
// returns a function that ends it. While it is held, a COMMIT elsewhere
// cannot finish.
func holdReadLock(t *testing.T) (release func()) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(Home(), "changes.db")+"?_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM changes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return func() {
		conn.ExecContext(ctx, "ROLLBACK")
		conn.Close()
		db.Close()
	}
}

func shortBusyTimeout(t *testing.T) {
	old := busyTimeoutMS
	busyTimeoutMS = 100
	t.Cleanup(func() { busyTimeoutMS = old })
}

// A reader that holds its lock a little longer than one busy timeout must
// not make the restore give up: the file is already in place, so COMMIT is
// retried.
func TestRestoreCommitRetriedWhileReaderBusy(t *testing.T) {
	shortBusyTimeout(t)
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	testHookAfterRestoreWrite = func() {
		release := holdReadLock(t)
		go func() { time.Sleep(350 * time.Millisecond); release() }()
	}
	defer func() { testHookAfterRestoreWrite = nil }()

	if _, _, err := s.Restore(good.ID); err != nil {
		t.Fatalf("restore gave up while the reader was busy: %v", err)
	}
	cs, _ := s.List(p, 1)
	if len(cs) != 1 || cs[0].Origin != OriginRestore {
		t.Fatalf("restore row missing: %+v", cs)
	}
}

// If COMMIT never succeeds, the transaction must be ended and the connection
// must not go back to the pool holding the lock: the same store keeps
// working and other connections can write.
func TestFailedCommitLeavesNoOpenTransaction(t *testing.T) {
	shortBusyTimeout(t)
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	var release func()
	testHookAfterRestoreWrite = func() { release = holdReadLock(t) }
	defer func() { testHookAfterRestoreWrite = nil }()

	_, _, err := s.Restore(good.ID)
	release()
	if err == nil || !strings.Contains(err.Error(), "file restored but not recorded") {
		t.Fatalf("got %v", err)
	}
	cs, _ := s.List(p, 0)
	for _, c := range cs {
		if c.Origin == OriginRestore {
			t.Fatalf("uncommitted restore row visible: %+v", cs)
		}
	}
	q := filepath.Join(dir, "other")
	write(t, q, "x\n", 0o644)
	if _, _, err := s.Snapshot(q, OriginManual, ""); err != nil {
		t.Fatalf("store unusable after a failed commit: %v", err)
	}
	other, err := Open(Home())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	write(t, q, "y\n", 0o644)
	if _, _, err := other.Snapshot(q, OriginManual, ""); err != nil {
		t.Fatalf("another connection cannot write after a failed commit: %v", err)
	}
}

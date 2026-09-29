package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

	_, prev, err := s.Restore(good.ID)
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
	// The overwritten content must still be reachable by the id the error
	// names, so the user can undo the restore.
	if prev == nil || !strings.Contains(err.Error(), "previous content saved as "+prev.ID) {
		t.Fatalf("error does not name the saved previous content: %v (prev %+v)", err, prev)
	}
	saved, err := s.Get(prev.ID)
	if err != nil {
		t.Fatalf("previous content not in history: %v", err)
	}
	if data, _ := s.Blob(saved.Blob); string(data) != "bad\n" {
		t.Fatalf("saved previous content: %q", data)
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

// A manual snapshot taken while a restore is committing must wait for it and
// see the restore row, not record the restored content as a new change.
func TestSnapshotDuringRestoreIsUnchanged(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	other, err := Open(Home())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	type result struct {
		unchanged bool
		err       error
	}
	done := make(chan result, 1)
	testHookAfterRestoreWrite = func() {
		go func() {
			_, unchanged, err := other.Snapshot(p, OriginManual, "concurrent")
			done <- result{unchanged, err}
		}()
		// Give the snapshot time to read the restored file and reach the
		// write lock before the restore commits.
		time.Sleep(300 * time.Millisecond)
	}
	defer func() { testHookAfterRestoreWrite = nil }()

	if _, _, err := s.Restore(good.ID); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || !r.unchanged {
		t.Fatalf("concurrent snapshot: unchanged=%v err=%v", r.unchanged, r.err)
	}
	cs, _ := s.List(p, 0)
	for _, c := range cs {
		if c.Intent == "concurrent" {
			t.Fatalf("extra row recorded during the restore: %+v", c)
		}
	}
}

// The slow part of a restore (writing and syncing the temp file) must happen
// before the write lock is taken, so a slow disk does not hold up other sc
// commands.
func TestRestorePreparesBeforeTakingLock(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	hookRan := false
	testHookBeforeRestoreLock = func() {
		hookRan = true
		tmps, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*"))
		if len(tmps) != 1 {
			t.Fatalf("temp files before the lock: %v", tmps)
		}
		if b, _ := os.ReadFile(tmps[0]); string(b) != "good\n" {
			t.Errorf("temp file not complete before the lock: %q", b)
		}
		if b, _ := os.ReadFile(p); string(b) != "bad\n" {
			t.Errorf("target changed before the lock: %q", b)
		}
		other, err := sql.Open("sqlite", "file:"+filepath.Join(Home(), "changes.db")+"?_pragma=busy_timeout(100)")
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		ctx := context.Background()
		conn, err := other.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var pre int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM changes WHERE origin = 'pre-restore'`).Scan(&pre); err != nil || pre != 1 {
			t.Errorf("pre-restore row not committed before the restore lock: %d %v", pre, err)
		}
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			t.Errorf("write lock already held while the temp file is prepared: %v", err)
			return
		}
		conn.ExecContext(ctx, "ROLLBACK")
	}
	defer func() { testHookBeforeRestoreLock = nil }()

	if _, _, err := s.Restore(good.ID); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("hook did not run")
	}
	if b, _ := os.ReadFile(p); string(b) != "good\n" {
		t.Fatalf("restored content: %q", b)
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*")); len(tmps) != 0 {
		t.Fatalf("temp files left: %v", tmps)
	}
}

// A restore that fails after the current file was saved keeps that saved
// state (it is the undo point), records no restore row and leaves the file
// unchanged.
func TestFailedRestoreKeepsPreviousContent(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	// Remove the prepared temp file so the rename fails under the lock.
	testHookBeforeRestoreLock = func() {
		tmps, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*"))
		for _, tmp := range tmps {
			os.Remove(tmp)
		}
	}
	defer func() { testHookBeforeRestoreLock = nil }()

	_, prev, err := s.Restore(good.ID)
	if err == nil || !strings.Contains(err.Error(), "file not changed") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Fatalf("file changed by a failed restore: %q", b)
	}
	cs, _ := s.List(p, 0)
	if len(cs) != 2 || cs[0].Origin != OriginPreRestore || cs[1].ID != good.ID {
		t.Fatalf("history after a failed restore: %+v", cs)
	}
	if prev == nil || prev.ID != cs[0].ID {
		t.Fatalf("prev: %+v", prev)
	}
	if data, _ := s.Blob(cs[0].Blob); string(data) != "bad\n" {
		t.Fatalf("pre-restore content: %q", data)
	}
}

// The pre-restore row must describe the file as it was right before this
// restore replaced it, even if another restore ran in between.
func TestPreRestoreSeesConcurrentRestore(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "a\n", 0o644)
	a, _ := snap(t, s, p)
	write(t, p, "b\n", 0o644)
	b, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	other, err := Open(Home())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// While restore A is prepared but has not taken the lock, restore B
	// runs to completion.
	testHookBeforeRestoreLock = func() {
		testHookBeforeRestoreLock = nil
		if _, _, err := other.Restore(b.ID); err != nil {
			t.Errorf("concurrent restore: %v", err)
		}
	}
	defer func() { testHookBeforeRestoreLock = nil }()

	_, prev, err := s.Restore(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil || prev.Blob != b.Blob {
		t.Fatalf("pre-restore row does not show restore B's result: %+v", prev)
	}
}

// When the database stays locked, the error says it was the restore that
// failed and that the file was not changed.
func TestRestoreLockedErrorSaysFileUnchanged(t *testing.T) {
	shortBusyTimeout(t)
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	db, err := sql.Open("sqlite", "file:"+filepath.Join(Home(), "changes.db")+"?_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Restore(good.ID)
	conn.ExecContext(ctx, "ROLLBACK")
	if err == nil || !strings.Contains(err.Error(), "restore "+p) || !strings.Contains(err.Error(), "file not changed") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Fatalf("file changed: %q", b)
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*")); len(tmps) != 0 {
		t.Fatalf("temp files left: %v", tmps)
	}
}

// A panic inside a write transaction must not leave the connection pooled
// with the transaction open.
func TestWriteTxPanicLeavesNoOpenTransaction(t *testing.T) {
	s, dir := setup(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic was swallowed")
			}
		}()
		s.writeTx(false, func(ctx context.Context, conn *sql.Conn) error {
			conn.ExecContext(ctx, `INSERT INTO changes (id, ts, path, blob, size, mode, uid, gid, origin)
				VALUES ('dead00', 0, '/x', 'b', 0, 0, 0, 0, 'manual')`)
			panic("boom")
		})
	}()
	p := filepath.Join(dir, "conf")
	write(t, p, "x\n", 0o644)
	if _, _, err := s.Snapshot(p, OriginManual, ""); err != nil {
		t.Fatalf("store unusable after a panic: %v", err)
	}
	if _, err := s.Get("dead00"); err == nil {
		t.Fatal("the panicking transaction's row was committed")
	}
}

// A snapshot has changed nothing on disk, so it gives up after one busy
// timeout instead of holding SQLite's lock through many COMMIT retries.
func TestSnapshotDoesNotRetryCommit(t *testing.T) {
	shortBusyTimeout(t)
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "x\n", 0o644)
	release := holdReadLock(t)
	defer release()
	commits := countCommits(t)
	_, _, err := s.Snapshot(p, OriginManual, "")
	if err == nil || !isBusy(err) {
		t.Fatalf("got %v", err)
	}
	if *commits != 1 {
		t.Fatalf("snapshot tried COMMIT %d times, want 1", *commits)
	}
}

// countCommits counts writeTx's COMMIT attempts until the test ends. The
// tests count attempts rather than time them: on a slow disk (a VM with
// fsync at 25-340 ms) a timing bound fails without any retry.
func countCommits(t *testing.T) *int {
	n := 0
	testHookBeforeCommit = func() { n++ }
	t.Cleanup(func() { testHookBeforeCommit = nil })
	return &n
}

// lockCheckingClock returns a clock that records an error whenever it is
// read while the database write lock is not held. Timestamps must be taken
// under the lock, so that rows are logged in the order they commit.
func lockCheckingClock(t *testing.T) func() time.Time {
	t.Helper()
	probe, err := sql.Open("sqlite", "file:"+filepath.Join(Home(), "changes.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { probe.Close() })
	tick := time.Unix(1_700_000_000, 0)
	return func() time.Time {
		ctx := context.Background()
		conn, err := probe.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
			conn.ExecContext(ctx, "ROLLBACK")
			t.Error("timestamp taken without holding the write lock")
		}
		tick = tick.Add(time.Second)
		return tick
	}
}

func TestRestoreTimestampsTakenUnderLock(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)
	s.now = lockCheckingClock(t)
	if _, _, err := s.Restore(good.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotTimestampTakenUnderLock(t *testing.T) {
	s, dir := setup(t)
	s.now = lockCheckingClock(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "x\n", 0o644)
	snap(t, s, p)
}

// If the file changes between being read and the write lock being taken,
// the snapshot reads it again and records what is on disk now.
func TestSnapshotRereadsFileChangedBeforeLock(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "first\n", 0o644)
	testHookSnapshotBeforeLock = func() {
		testHookSnapshotBeforeLock = nil
		write(t, p, "second, longer\n", 0o644)
	}
	defer func() { testHookSnapshotBeforeLock = nil }()
	c, _ := snap(t, s, p)
	if data, _ := s.Blob(c.Blob); string(data) != "second, longer\n" {
		t.Fatalf("recorded %q, the file on disk is the second version", data)
	}
	cs, _ := s.List(p, 0)
	if len(cs) != 1 {
		t.Fatalf("rows: %+v", cs)
	}
}

// A file that never stops changing gives one clear error, not a wrong row.
func TestSnapshotGivesUpOnFileThatKeepsChanging(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "0\n", 0o644)
	n := 0
	testHookSnapshotBeforeLock = func() { n++; write(t, p, strings.Repeat("x", n)+"\n", 0o644) }
	defer func() { testHookSnapshotBeforeLock = nil }()
	_, _, err := s.Snapshot(p, OriginManual, "")
	if err == nil || !strings.Contains(err.Error(), "kept changing") {
		t.Fatalf("got %v", err)
	}
	if cs, _ := s.List(p, 0); len(cs) != 0 {
		t.Fatalf("rows recorded for a file that kept changing: %+v", cs)
	}
}

func resetInterrupt(t *testing.T) {
	t.Cleanup(func() {
		interrupted.Store(false)
		pointMu.Lock()
		forced, passedPoint = false, false
		pointMu.Unlock()
	})
}

// Interrupted before the rename: nothing on disk changes and the message
// says so.
func TestInterruptBeforeRenameChangesNothing(t *testing.T) {
	resetInterrupt(t)
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)
	testHookBeforeRestoreLock = func() { Interrupt() }
	defer func() { testHookBeforeRestoreLock = nil }()
	_, _, err := s.Restore(good.ID)
	if err == nil || !errors.Is(err, ErrInterrupted) && !strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), "file not changed") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Fatalf("file changed: %q", b)
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*")); len(tmps) != 0 {
		t.Fatalf("temp files left: %v", tmps)
	}
}

// Interrupted after the rename, while COMMIT is being retried: it stops
// retrying at once and reports that the file was replaced and where the
// previous content is.
func TestInterruptAfterRenameStopsRetries(t *testing.T) {
	resetInterrupt(t)
	old := busyTimeoutMS
	busyTimeoutMS = 300
	t.Cleanup(func() { busyTimeoutMS = old })
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)
	var release func()
	commits := countCommits(t)
	testHookAfterRestoreWrite = func() { release = holdReadLock(t); Interrupt(); *commits = 0 }
	defer func() { testHookAfterRestoreWrite = nil }()
	_, prev, err := s.Restore(good.ID)
	release()
	if err == nil || !strings.Contains(err.Error(), "file restored but not recorded") || prev == nil || !strings.Contains(err.Error(), prev.ID) {
		t.Fatalf("got %v (prev %+v)", err, prev)
	}
	if *commits != 1 {
		t.Fatalf("tried COMMIT %d times after the interrupt, want 1", *commits)
	}
}

func TestInterruptedSnapshotRecordsNothing(t *testing.T) {
	resetInterrupt(t)
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "x\n", 0o644)
	testHookSnapshotBeforeLock = func() { Interrupt() }
	defer func() { testHookSnapshotBeforeLock = nil }()
	_, _, err := s.Snapshot(p, OriginManual, "")
	if err == nil || !errors.Is(err, ErrInterrupted) || !strings.Contains(err.Error(), "nothing recorded") {
		t.Fatalf("got %v", err)
	}
	if cs, _ := s.List(p, 0); len(cs) != 0 {
		t.Fatalf("rows: %+v", cs)
	}
}

// A file replaced while it is being read is retried like one that changed
// after the read.
func TestRetryableErrors(t *testing.T) {
	if !retryable(errChanged) || !retryable(fmt.Errorf("x: %w", fsutil.ErrReplaced)) {
		t.Fatal("changed or replaced files must be retried")
	}
	if retryable(errors.New("permission denied")) || retryable(ErrInterrupted) {
		t.Fatal("other errors must not be retried")
	}
}

// Restore puts back setuid, setgid and (where the kernel lets a non-root
// owner set it) sticky bits exactly as recorded.
func TestRestoreSpecialModeBits(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "tool")
	write(t, p, "#!/bin/sh\n", 0o755)
	want := os.FileMode(0o755) | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	os.Chmod(p, want)
	fi, _ := os.Stat(p)
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != os.ModeSetuid|os.ModeSetgid {
		t.Skipf("cannot set setuid/setgid here: %v", fi.Mode())
	}
	recorded := fi.Mode()
	c, _ := snap(t, s, p)
	if c.Mode != recorded {
		t.Fatalf("recorded mode %v, file has %v", c.Mode, recorded)
	}
	os.Chmod(p, 0o644)
	write(t, p, "changed\n", 0o644)
	if _, _, err := s.Restore(c.ID); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(p)
	if fi.Mode() != recorded {
		t.Fatalf("restored mode %v, want %v", fi.Mode(), recorded)
	}
}

// Ids follow the spec: the first 6 hex of sha256(path \n ts \n blob), with
// "\n<n>" appended on a collision.
func TestIDFormula(t *testing.T) {
	path, blob := "/etc/hosts", strings.Repeat("ab", 32)
	sum := sha256.Sum256([]byte(path + "\n1700000000\n" + blob))
	if got, want := makeID(path, 1_700_000_000, blob, 0), hex.EncodeToString(sum[:])[:6]; got != want {
		t.Fatalf("makeID = %s, want %s", got, want)
	}
	sum = sha256.Sum256([]byte(path + "\n1700000000\n" + blob + "\n1"))
	if got, want := makeID(path, 1_700_000_000, blob, 1), hex.EncodeToString(sum[:])[:6]; got != want {
		t.Fatalf("makeID attempt 1 = %s, want %s", got, want)
	}
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "x\n", 0o644)
	c, _ := snap(t, s, p)
	if c.ID != makeID(c.Path, c.TS, c.Blob, 0) {
		t.Fatalf("stored id %s does not follow the formula", c.ID)
	}
}

// The file is deleted after its state was saved and before the restore
// takes the lock: the restore starts over, finds no file, and creates it.
func TestRestoreFileDeletedBeforeLock(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)
	testHookBeforeRestoreLock = func() { testHookBeforeRestoreLock = nil; os.Remove(p) }
	defer func() { testHookBeforeRestoreLock = nil }()
	_, prev, err := s.Restore(good.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev != nil {
		t.Fatalf("prev for a file that no longer existed: %+v", prev)
	}
	if b, _ := os.ReadFile(p); string(b) != "good\n" {
		t.Fatalf("content %q", b)
	}
	cs, _ := s.List(p, 0)
	if len(cs) != 3 || cs[0].Origin != OriginRestore || cs[1].Origin != OriginPreRestore {
		t.Fatalf("history: %+v", cs)
	}
	if data, _ := s.Blob(cs[1].Blob); string(data) != "bad\n" {
		t.Fatalf("the deleted content was not kept: %q", data)
	}
}

// The file appears after the restore found none and before it takes the
// lock: the restore starts over and saves the new file first.
func TestRestoreFileCreatedBeforeLock(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	os.Remove(p)
	testHookBeforeRestoreLock = func() { testHookBeforeRestoreLock = nil; write(t, p, "new\n", 0o644) }
	defer func() { testHookBeforeRestoreLock = nil }()
	_, prev, err := s.Restore(good.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil {
		t.Fatal("the new file was overwritten without being saved")
	}
	if data, _ := s.Blob(prev.Blob); string(data) != "new\n" {
		t.Fatalf("saved %q", data)
	}
}

// A file that keeps changing makes the restore give up after 3 attempts,
// without touching it.
func TestRestoreGivesUpWhenFileKeepsChanging(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	n := 0
	testHookBeforeRestoreLock = func() { n++; write(t, p, strings.Repeat("x", n)+"\n", 0o644) }
	defer func() { testHookBeforeRestoreLock = nil }()
	_, _, err := s.Restore(good.ID)
	if err == nil || !strings.Contains(err.Error(), "kept changing") || !strings.Contains(err.Error(), "file not changed") {
		t.Fatalf("got %v", err)
	}
	if n != maxAttempts {
		t.Fatalf("attempts: %d, want %d", n, maxAttempts)
	}
	if b, _ := os.ReadFile(p); string(b) != strings.Repeat("x", n)+"\n" {
		t.Fatalf("file touched: %q", b)
	}
	cs, _ := s.List(p, 0)
	for _, c := range cs {
		if c.Origin == OriginRestore {
			t.Fatalf("restore row recorded: %+v", cs)
		}
	}
}

// ForceStop lets the process exit only while no restore has passed its point
// of no return, and stops later restores before their rename.
func TestForceStop(t *testing.T) {
	resetInterrupt(t)
	pointMu.Lock()
	forced, passedPoint = false, false
	pointMu.Unlock()
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "good\n", 0o644)
	good, _ := snap(t, s, p)
	write(t, p, "bad\n", 0o644)

	if !ForceStop() {
		t.Fatal("ForceStop refused with no restore running")
	}
	_, _, err := s.Restore(good.ID)
	if err == nil || !strings.Contains(err.Error(), "file not changed") {
		t.Fatalf("restore after ForceStop: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Fatalf("file changed after ForceStop: %q", b)
	}

	// A restore that has passed its point of no return makes ForceStop wait.
	pointMu.Lock()
	forced = false
	pointMu.Unlock()
	var allowed bool
	testHookAfterRestoreWrite = func() { allowed = ForceStop() }
	defer func() { testHookAfterRestoreWrite = nil }()
	if _, _, err := s.Restore(good.ID); err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("ForceStop allowed an exit right after a rename")
	}
}

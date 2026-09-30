package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// M1's SQL, copied from tag m1. The M1 binary keeps running these texts on
// stores that M2 has migrated, so they are frozen here, not taken from the
// current code.
const (
	m1Schema = `
CREATE TABLE IF NOT EXISTS changes (
  id     TEXT PRIMARY KEY,
  ts     INTEGER NOT NULL,
  path   TEXT NOT NULL,
  blob   TEXT NOT NULL,
  size   INTEGER NOT NULL,
  mode   INTEGER NOT NULL,
  uid    INTEGER NOT NULL,
  gid    INTEGER NOT NULL,
  origin TEXT NOT NULL,
  intent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS changes_path_ts ON changes(path, ts DESC);
`
	m1Insert = `INSERT INTO changes (id, ts, path, blob, size, mode, uid, gid, origin, intent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	m1List   = `SELECT id, ts, path, blob, size, mode, uid, gid, origin, intent FROM changes WHERE path = ? ORDER BY ts DESC, rowid DESC`
	m1Latest = `SELECT id, ts, path, blob, size, mode, uid, gid, origin, intent FROM changes WHERE path = ?
		ORDER BY ts DESC, rowid DESC LIMIT 1`
	m1Prefix = `SELECT id, ts, path, blob, size, mode, uid, gid, origin, intent FROM changes WHERE id LIKE ? ORDER BY id LIMIT 11`
)

// m1IDs are the rows of m1Store, in insert order, all in the same second
// (like the real store's four fstab rows).
var m1IDs = []string{"2c6901", "9a0b1c", "07d3e2"}

// m1Store makes a store as M1 left it: M1's DDL at schema version 0, a
// 0644 database and three same-second rows for one path.
func m1Store(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SC_HOME", home)
	db := rawDB(t, home)
	if _, err := db.Exec(m1Schema); err != nil {
		t.Fatal(err)
	}
	for i, id := range m1IDs {
		blob := strings.Repeat(string(rune('a'+i)), 64)
		if _, err := db.Exec(m1Insert, id, 1_700_000_000, "/etc/fstab", blob, 446, 0o644, 0, 0, "manual", ""); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if err := os.Chmod(filepath.Join(home, "changes.db"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// rawDB opens the store's database directly, for setting up and inspecting.
func rawDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "changes.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func queryInt(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func hasColumn(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	return queryInt(t, db, `SELECT count(*) FROM pragma_table_info('changes') WHERE name = ?`, name) == 1
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMigrateM1Store(t *testing.T) {
	home := m1Store(t)
	original := readFile(t, filepath.Join(home, "changes.db"))
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.List("", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cs {
		got = append(got, c.ID)
	}
	if want := []string{m1IDs[2], m1IDs[1], m1IDs[0]}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ids newest first: %v, want %v", got, want)
	}
	s.Close()

	db := rawDB(t, home)
	if v := queryInt(t, db, "PRAGMA user_version"); v != 1 {
		t.Fatalf("schema version %d, want 1", v)
	}
	if n := queryInt(t, db, `SELECT count(*) FROM changes WHERE kind = 'file' AND target = ''`); n != len(m1IDs) {
		t.Fatalf("%d rows of kind file with no target, want %d", n, len(m1IDs))
	}
	if n := queryInt(t, db, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('changes_path_seq', 'changes_path_ts')`); n != 2 {
		t.Fatalf("%d of the 2 indexes present", n)
	}
	backup := filepath.Join(home, m1Backup)
	fi, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v, want 0600", fi.Mode().Perm())
	}
	if !bytes.Equal(readFile(t, backup), original) {
		t.Fatal("backup differs from the M1 database")
	}

	// Opening again changes nothing.
	dbBefore := readFile(t, filepath.Join(home, "changes.db"))
	s, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if !bytes.Equal(readFile(t, filepath.Join(home, "changes.db")), dbBefore) {
		t.Fatal("reopening changed the database")
	}
	if fi2, _ := os.Stat(backup); !fi2.ModTime().Equal(fi.ModTime()) || !bytes.Equal(readFile(t, backup), original) {
		t.Fatal("reopening rewrote the backup")
	}
}

// Several sc processes may open an M1 store for the first time at once
// (sc and scd at boot): all succeed and the store is migrated once.
func TestMigrateConcurrentFirstOpens(t *testing.T) {
	home := m1Store(t)
	original := readFile(t, filepath.Join(home, "changes.db"))
	errs := make([]error, 4)
	// Every open reads version 0 before any takes the lock, so all but the
	// first find the store migrated only when they read it again under it.
	var read sync.WaitGroup
	read.Add(len(errs))
	allRead := make(chan struct{})
	go func() { read.Wait(); close(allRead) }()
	testHookBeforeMigrateLock = func() {
		read.Done()
		select {
		case <-allRead:
		case <-time.After(10 * time.Second):
			t.Error("not every open reached the lock")
		}
	}
	defer func() { testHookBeforeMigrateLock = nil }()
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Open(home)
			if err == nil {
				s.Close()
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	db := rawDB(t, home)
	if v := queryInt(t, db, "PRAGMA user_version"); v != 1 {
		t.Fatalf("schema version %d, want 1", v)
	}
	if n := queryInt(t, db, "SELECT count(*) FROM changes"); n != len(m1IDs) {
		t.Fatalf("%d rows, want %d", n, len(m1IDs))
	}
	if !bytes.Equal(readFile(t, filepath.Join(home, m1Backup)), original) {
		t.Fatal("backup differs from the M1 database")
	}
}

func TestMigrateRefusesNewerStore(t *testing.T) {
	home := m1Store(t)
	db := rawDB(t, home)
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	_, err := Open(home)
	if err == nil || err.Error() != "store "+home+" was written by a newer sc (schema 2)" {
		t.Fatalf("got %v", err)
	}
	if v := queryInt(t, db, "PRAGMA user_version"); v != 2 {
		t.Fatalf("schema version changed to %d", v)
	}
}

// A migration that fails part way leaves the store as M1 left it, and the
// next open migrates it.
func TestMigrateFailureChangesNothing(t *testing.T) {
	home := m1Store(t)
	testHookMigrate = func(v int) error {
		if v == 1 {
			return errors.New("injected failure")
		}
		return nil
	}
	defer func() { testHookMigrate = nil }()
	if _, err := Open(home); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("got %v", err)
	}
	db := rawDB(t, home)
	if v := queryInt(t, db, "PRAGMA user_version"); v != 0 {
		t.Fatalf("schema version %d after a failed migration, want 0", v)
	}
	if hasColumn(t, db, "kind") || hasColumn(t, db, "target") {
		t.Fatal("a failed migration left new columns")
	}
	if n := queryInt(t, db, "SELECT count(*) FROM changes"); n != len(m1IDs) {
		t.Fatalf("%d rows, want %d", n, len(m1IDs))
	}
	testHookMigrate = nil
	s, err := Open(home)
	if err != nil {
		t.Fatalf("open after the failure: %v", err)
	}
	s.Close()
	if !hasColumn(t, db, "kind") {
		t.Fatal("not migrated on the next open")
	}
}

// The M1 binary's statements still work on a migrated store, and its rows
// get kind 'file'.
func TestM1StatementsWorkOnSchema1(t *testing.T) {
	home := m1Store(t)
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db := rawDB(t, home)
	if _, err := db.Exec(m1Insert, "ffff01", 1_700_000_001, "/etc/fstab", strings.Repeat("f", 64), 10, 0o600, 0, 0, "manual", "by M1"); err != nil {
		t.Fatalf("M1 insert: %v", err)
	}
	for _, q := range []struct {
		sql  string
		arg  any
		rows int
	}{{m1List, "/etc/fstab", 4}, {m1Latest, "/etc/fstab", 1}, {m1Prefix, "ffff%", 1}} {
		rows, err := db.Query(q.sql, q.arg)
		if err != nil {
			t.Fatalf("M1 query %q: %v", q.sql, err)
		}
		n := 0
		for rows.Next() {
			var id, path, blob, origin, intent string
			var ts, size, mode, uid, gid int64
			if err := rows.Scan(&id, &ts, &path, &blob, &size, &mode, &uid, &gid, &origin, &intent); err != nil {
				t.Fatal(err)
			}
			n++
		}
		rows.Close()
		if n != q.rows {
			t.Fatalf("M1 query %q: %d rows, want %d", q.sql, n, q.rows)
		}
	}
	var kind string
	if err := db.QueryRow(`SELECT kind FROM changes WHERE id = 'ffff01'`).Scan(&kind); err != nil || kind != "file" {
		t.Fatalf("kind of an M1 row: %q, %v", kind, err)
	}
}

// The store keeps SQLite's rollback journal; a store switched to WAL is
// refused.
func TestJournalMode(t *testing.T) {
	setup(t)
	db := rawDB(t, Home())
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("journal mode %q, %v", mode, err)
	}
	if err := db.QueryRow("PRAGMA journal_mode = WAL").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("switch to WAL: %q, %v", mode, err)
	}
	db.Close()
	if _, err := Open(Home()); err == nil || !strings.Contains(err.Error(), "journal mode wal") {
		t.Fatalf("got %v", err)
	}
}

// A fresh store starts at the current schema and takes no backup.
func TestFreshStoreSchema(t *testing.T) {
	setup(t)
	db := rawDB(t, Home())
	if v := queryInt(t, db, "PRAGMA user_version"); v != schemaVersion {
		t.Fatalf("schema version %d, want %d", v, schemaVersion)
	}
	if !hasColumn(t, db, "kind") || !hasColumn(t, db, "target") {
		t.Fatal("columns kind and target missing")
	}
	if _, err := os.Stat(filepath.Join(Home(), m1Backup)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh store has a backup: %v", err)
	}
}

// helperEnv tells the test binary, run by a test as a separate process, what
// TestHelperProcess should do. SQLite's locks are POSIX locks, which only
// behave as they do in production between processes.
const helperEnv = "SC_STORE_TEST_HELPER"

// TestHelperProcess is not a test of its own: it is the other process.
// "open" opens the store in $SC_HOME; "write" tries to take the write lock
// for 100 ms and prints "locked" or "got the lock".
func TestHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		t.Skip("only run as a helper process")
	case "open":
		s, err := Open(Home())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		s.Close()
	case "record-crash":
		recordCrashHelper()
	case "write":
		db, err := sql.Open("sqlite", "file:"+filepath.Join(Home(), "changes.db")+"?_pragma=busy_timeout(100)")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
			if !isBusy(err) {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Print("locked")
		} else {
			db.Exec("ROLLBACK")
			fmt.Print("got the lock")
		}
		db.Close()
	}
	os.Exit(0)
}

// helper returns the test binary as a helper process doing mode on home.
func helper(mode, home string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode, "SC_HOME="+home)
	return cmd
}

// No other process may write while a migration runs, also after the M1
// backup (reading changes.db through a second descriptor would drop this
// process's locks).
func TestMigrateKeepsWriteLockFromOtherProcesses(t *testing.T) {
	home := m1Store(t)
	var got string
	testHookMigrate = func(v int) error {
		if v == 0 { // the backup is taken
			out, err := helper("write", home).Output()
			if err != nil {
				return fmt.Errorf("helper: %v", err)
			}
			got = string(out)
		}
		return nil
	}
	defer func() { testHookMigrate = nil }()
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got != "locked" {
		t.Fatalf("another process during the migration: %q, want locked", got)
	}
	if _, err := os.Stat(filepath.Join(home, m1Backup)); err != nil {
		t.Fatal(err)
	}
}

// sc and scd may open an M1 store for the first time at once: every process
// succeeds and the store is intact.
func TestMigrateConcurrentFirstOpensInProcesses(t *testing.T) {
	home := m1Store(t)
	cmds := make([]*exec.Cmd, 4)
	outs := make([]*bytes.Buffer, len(cmds))
	for i := range cmds {
		cmds[i] = helper("open", home)
		outs[i] = new(bytes.Buffer)
		cmds[i].Stderr = outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("process %d: %v: %s", i, err, outs[i])
		}
	}
	db := rawDB(t, home)
	if v := queryInt(t, db, "PRAGMA user_version"); v != 1 {
		t.Fatalf("schema version %d, want 1", v)
	}
	var ok string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q, %v", ok, err)
	}
}

// A newer sc that migrates the store while this one waits for the lock is
// seen under the lock: the store is refused, not set back to version 1.
func TestMigrateRefusesStoreUpgradedWhileWaiting(t *testing.T) {
	home := m1Store(t)
	db := rawDB(t, home)
	testHookBeforeMigrateLock = func() {
		if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
			t.Error(err)
		}
	}
	defer func() { testHookBeforeMigrateLock = nil }()
	if _, err := Open(home); err == nil || !strings.Contains(err.Error(), "newer sc (schema 2)") {
		t.Fatalf("got %v", err)
	}
	if v := queryInt(t, db, "PRAGMA user_version"); v != 2 {
		t.Fatalf("schema version set to %d", v)
	}
}

// Anything but a regular file at the backup's name (here a dangling
// symlink) stops the migration instead of leaving the store with no backup.
func TestMigrateBackupNameTaken(t *testing.T) {
	home := m1Store(t)
	if err := os.Symlink("/nonexistent/x", filepath.Join(home, m1Backup)); err != nil {
		t.Fatal(err)
	}
	_, err := Open(home)
	if err == nil || !strings.Contains(err.Error(), m1Backup+" is not a regular file") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("got %v", err)
	}
	if v := queryInt(t, rawDB(t, home), "PRAGMA user_version"); v != 0 {
		t.Fatalf("schema version %d, want 0", v)
	}
}

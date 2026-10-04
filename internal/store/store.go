// Package store keeps file snapshots: content-addressed blobs under objects/
// and one SQLite row per recorded change.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/scope"
)

// DefaultHome is used when $SC_HOME is unset.
const DefaultHome = "/var/lib/smartconfig"

// Origins of a change row.
const (
	OriginManual     = "manual"
	OriginPreRestore = "pre-restore"
	OriginRestore    = "restore"
	OriginEdit       = "edit" // written by sc edit (M3)
)

const idLen = 6

// schema is M1's schema, migration 0 (see migrate.go).
const schema = `
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

// Change is one recorded state of a file.
type Change struct {
	ID     string
	TS     int64
	Path   string
	Blob   string
	Size   int64
	Mode   os.FileMode
	UID    int
	GID    int
	Origin string
	Intent string
	Kind   string // KindFile, KindLink, KindDigest or KindDeleted
	Target string // link text, for KindLink
}

// busyTimeoutMS is how long one statement waits for another connection's
// lock before failing with SQLITE_BUSY. Tests shorten it.
var busyTimeoutMS = 5000

// commitAttempts is how many times writeTx tries COMMIT while other
// connections are busy, when the caller has already changed a file on disk
// and so must not give up easily. Each attempt waits up to busyTimeoutMS.
const commitAttempts = 6

// ErrInterrupted is returned when Interrupt was called before an operation
// reached a point of no return.
var ErrInterrupted = errors.New("interrupted")

var interrupted atomic.Bool

// Interrupt asks Snapshot and Restore calls in this process to stop at their
// next safe point: they start no new work and stop retrying, but a restore
// that has already renamed its file still reports that it did. It is safe to
// call from a signal handler goroutine.
func Interrupt() { interrupted.Store(true) }

func stopping() bool { return interrupted.Load() }

var (
	pointMu     sync.Mutex
	forced      bool // ForceStop was called
	passedPoint bool // a restore in this process went past its last safe point
)

// ForceStop is for a second signal. From now on no restore in this process
// may pass its point of no return (the rename). It reports whether the
// process may exit at once (true), or must let the running operation finish
// and report (false), because a restore may already have renamed its file.
func ForceStop() bool {
	pointMu.Lock()
	defer pointMu.Unlock()
	forced = true
	return !passedPoint
}

// testHookSnapshotBeforeLock, if set by a test, runs in Record (so in
// Snapshot) after the paths are read and before the write lock is taken.
var testHookSnapshotBeforeLock func()

// testHookBeforeRestoreLock, if set by a test, runs in Restore after the new
// content is prepared in a temp file and before the write lock is taken.
var testHookBeforeRestoreLock func()

// testHookAfterRestoreWrite, if set by a test, runs in Restore after the file
// is in place and before the restore row is committed.
var testHookAfterRestoreWrite func()

// testHookBeforeCommit, if set by a test, runs in writeTx before every
// COMMIT attempt.
var testHookBeforeCommit func()

// Store is an open SmartConfig data directory.
type Store struct {
	dir         string
	db          *sql.DB
	now         func() time.Time
	fingerprint func(path string) bool
	name        string // the store's own directory, for messages (dir is a copy's)
	readOnly    string // why sc may only read the store here; "" when it may write
	copyDir     string // a repaired private copy being read (a hot journal); removed by Close
}

// ErrReadOnly is returned by every write to a store sc may only read
// here: a read-only root (the rescue shell), a store this user may not
// write, or a repaired copy of a store a crash left half-written. The
// error says which.
var ErrReadOnly = errors.New("the store can only be read here")

// Home returns $SC_HOME, or DefaultHome when it is unset.
func Home() string {
	if h := os.Getenv("SC_HOME"); h != "" {
		return h
	}
	return DefaultHome
}

// Init creates dir (mode 0700), objects/ and changes.db. It is idempotent.
func Init(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	// The database holds every path and change, so only root may read it.
	// Creating the file before SQLite does sets its mode (SQLite gives the
	// journal the database's mode), and the chmod fixes a store made by M1,
	// whose database was 0644.
	db := filepath.Join(dir, "changes.db")
	f, err := os.OpenFile(db, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", db, err)
	}
	f.Close()
	if err := os.Chmod(db, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", db, err)
	}
	s, err := open(dir)
	if err != nil {
		return err
	}
	return s.Close()
}

// Open opens a data directory previously created by Init.
func Open(dir string) (*Store, error) {
	if _, err := os.Stat(filepath.Join(dir, "changes.db")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s is not initialised, run: sc init", dir)
		}
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return open(dir)
}

func open(dir string) (*Store, error) {
	file := filepath.Join(dir, "changes.db")
	// access(2) says no for a read-only mount (EROFS) as well as for
	// permissions (EACCES) and an immutable file (EPERM).
	err := syscall.Access(dir, 2 /* W_OK */)
	if err == nil {
		err = syscall.Access(file, 2)
	}
	if err == nil {
		return openDB(dir, file, dir, "")
	}
	why := "this user may not write it"
	if errors.Is(err, syscall.EROFS) {
		why = "it is on a read-only file system: remount it read-write first"
	}
	for attempt := 0; ; attempt++ {
		s, err := openDB(dir, file, dir, why)
		if err == nil || !hotJournal(err) {
			return s, err
		}
		// A crash left a write unfinished: SQLite must roll it back before
		// anything can be read, and that needs a write. Read a copy,
		// repaired in a private directory; the store itself is repaired by
		// the next sc run that may write it. A journal gone by the time of
		// the copy was rolled back by such a run: read the store again.
		s, err = openCopy(dir, why)
		if !errors.Is(err, errJournalGone) || attempt > 0 {
			return s, err
		}
	}
}

// openDB opens the database file of the store in dir; name is the store's
// own directory, for messages. why, when not "", says why sc may only
// read the store: the file is opened with mode=ro, and every write,
// a migration included, fails with ErrReadOnly and why.
func openDB(dir, file, name, why string) (*Store, error) {
	dsn := "file:" + file + "?_pragma=busy_timeout(" + strconv.Itoa(busyTimeoutMS) + ")&_txlock=immediate"
	if why != "" && name == dir {
		dsn += "&mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{dir: dir, name: name, db: db, now: time.Now, fingerprint: scope.Default().FingerprintOnly, readOnly: why}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// hotJournal reports whether err is SQLite's "attempt to write a readonly
// database" for a journal it must roll back (SQLITE_READONLY_ROLLBACK).
func hotJournal(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_READONLY_ROLLBACK
}

// copyParents are the places tried for a repaired copy: tmpfs first,
// since a store sc may not write is most likely on a read-only root.
// Tests point it elsewhere.
var copyParents = fsutil.TempParents

// errJournalGone: the journal a copy needed was rolled back meanwhile.
var errJournalGone = errors.New("the journal is gone")

// openCopy copies the database file of the store in dir and its journal
// into a new private directory, links objects/ back to the store, and
// opens the copy: SQLite rolls the unfinished write back there. The store
// in dir is not changed, and the copy can only be read (why). The copy is
// removed by Close, or by fsutil.RemovePending when a signal ends sc.
func openCopy(dir, why string) (*Store, error) {
	tmp, err := fsutil.PrivateDir("sc-store-", copyParents())
	if err != nil {
		return nil, fmt.Errorf("store %s has an unfinished write, and no copy of it could be made to read: %w", dir, err)
	}
	fail := func(err error) (*Store, error) {
		fsutil.RemoveDir(tmp)
		if errors.Is(err, errJournalGone) {
			return nil, err
		}
		return nil, fmt.Errorf("store %s has an unfinished write, and no copy of it could be made to read: %w", dir, err)
	}
	for _, name := range []string{"changes.db", "changes.db-journal"} {
		if err := copyFile(filepath.Join(dir, name), filepath.Join(tmp, name)); err != nil {
			if name == "changes.db-journal" && errors.Is(err, fs.ErrNotExist) {
				err = errJournalGone
			}
			return fail(err)
		}
	}
	abs, err := filepath.Abs(filepath.Join(dir, "objects"))
	if err != nil {
		return fail(err)
	}
	if err := os.Symlink(abs, filepath.Join(tmp, "objects")); err != nil {
		return fail(err)
	}
	s, err := openDB(tmp, filepath.Join(tmp, "changes.db"), dir,
		"it has a write a crash left unfinished, and sc reads a repaired copy: "+why)
	if err != nil {
		fsutil.RemoveDir(tmp)
		return nil, err
	}
	s.copyDir = tmp
	return s, nil
}

// copyFile copies src to a new file dst, mode 0600, without holding it
// in memory.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// NewestRowID returns the rowid of the newest row, 0 for an empty store.
// Rows are never deleted, so rowid order is insert order: the rows after
// one are exactly what was recorded after it.
func (s *Store) NewestRowID() (int64, error) {
	var n int64
	err := s.db.QueryRow("SELECT coalesce(max(rowid), 0) FROM changes").Scan(&n)
	return n, err
}

// Copied reports whether this is a repaired copy of a store a crash left
// half-written (see open): its rows are the store's, as of before that
// write.
func (s *Store) Copied() bool { return s.copyDir != "" }

// readOnlyErr is the error of a write to a store sc may only read here.
func (s *Store) readOnlyErr() error {
	return fmt.Errorf("store %s: %w: %s", s.name, ErrReadOnly, s.readOnly)
}

// Close closes the database and removes a repaired copy.
func (s *Store) Close() error {
	err := s.db.Close()
	if s.copyDir != "" {
		fsutil.RemoveDir(s.copyDir)
	}
	return err
}

// Snapshot records the current content of path. For a manual snapshot whose
// content equals the newest recorded one, nothing is inserted and that row is
// returned with unchanged=true. Other origins always insert a row.
func (s *Store) Snapshot(path, origin, intent string) (c Change, unchanged bool, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return Change{}, false, fmt.Errorf("resolve path: %w", err)
	}
	for attempt := 1; ; attempt++ {
		if stopping() {
			return Change{}, false, fmt.Errorf("snapshot %s: %w (nothing recorded)", path, ErrInterrupted)
		}
		c, unchanged, err = s.snapshotOnce(path, origin, intent)
		if errors.Is(err, ErrInterrupted) {
			return Change{}, false, fmt.Errorf("snapshot %s: %w (nothing recorded)", path, ErrInterrupted)
		}
		if !retryable(err) {
			return c, unchanged, err
		}
		if attempt == maxAttempts {
			return Change{}, false, fmt.Errorf("%s kept changing while being read, try again", path)
		}
	}
}

// snapshotOnce reads path and records it through Record, checking under
// the write lock that the path is still the version read (else errChanged).
// A manual snapshot equal to the newest row inserts nothing and returns that
// row with unchanged=true; other origins always insert.
func (s *Store) snapshotOnce(path, origin, intent string) (c Change, unchanged bool, err error) {
	st, err := fsutil.ReadState(path)
	if err != nil {
		return Change{}, false, err
	}
	if !st.Stable {
		return Change{}, false, errChanged
	}
	res, err := s.Record([]Obs{{Path: path, State: &st, CheckStamp: true,
		Force: origin != OriginManual, Origin: origin, Intent: intent}})
	if err != nil {
		return Change{}, false, err
	}
	if res[0].Moved {
		return Change{}, false, errChanged
	}
	return res[0].Change, !res[0].Recorded, nil
}

// latestTx returns the newest change of path within the caller's
// transaction, or nil if there is none. Newest means last inserted: ts is
// wall-clock time, which can step back (NTP, a paused VM), so it only
// labels a row. Rows are never deleted and the store is never vacuumed, so
// rowid order is insert order.
func latestTx(ctx context.Context, conn *sql.Conn, path string) (*Change, error) {
	rows, err := conn.QueryContext(ctx, `SELECT `+cols+` FROM changes WHERE path = ?
		ORDER BY rowid DESC LIMIT 1`, path)
	if err != nil {
		return nil, fmt.Errorf("read latest change: %w", err)
	}
	cs, err := scanChanges(rows)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// makeID derives an id from path, timestamp and key: the blob for file rows
// (so M1's ids are reproduced), "link\n"+target for links and "deleted" for
// deletions. If that id is taken, attempt n>0 appends "\n<n>" to the hashed
// text. Digest rows get randomID instead.
func makeID(path string, ts int64, key string, attempt int) string {
	text := path + "\n" + strconv.FormatInt(ts, 10) + "\n" + key
	if attempt > 0 {
		text += "\n" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:idLen]
}

// randomID is a digest row's id. The journal prints ids, and one derived
// from the fingerprint would let a reader test guesses at a short secret.
func randomID() string {
	b := make([]byte, idLen/2)
	rand.Read(b) // crypto/rand: never returns an error
	return hex.EncodeToString(b)
}

// idKey is the part of a row's id that stands for its state (plan 5.2).
func idKey(c *Change) string {
	switch c.Kind {
	case KindLink:
		return "link\n" + c.Target
	case KindDeleted:
		return "deleted"
	}
	return c.Blob
}

// writeTx runs fn inside one write transaction (BEGIN IMMEDIATE, so the
// database write lock is held from the start) on a connection of its own.
// If fn fails or panics, the transaction is rolled back and fn's error is
// returned as is. With retryCommit (for callers whose fn has already changed
// a file on disk), COMMIT is retried while other connections keep it busy;
// otherwise one busy timeout is enough to give up. A connection whose
// transaction cannot be ended is discarded, never handed back to the pool
// still holding the lock (database/sql's Tx does not guarantee that after a
// failed COMMIT).
func (s *Store) writeTx(retryCommit bool, fn func(ctx context.Context, conn *sql.Conn) error) (err error) {
	if s.readOnly != "" {
		return s.readOnlyErr()
	}
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("database connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if stopping() {
			return ErrInterrupted
		}
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			rollback(ctx, conn)
			panic(p)
		}
	}()
	if err := fn(ctx, conn); err != nil {
		rollback(ctx, conn)
		return err
	}
	attempts := 1
	if retryCommit {
		attempts = commitAttempts
	}
	for attempt := 1; ; attempt++ {
		if testHookBeforeCommit != nil {
			testHookBeforeCommit()
		}
		_, err = conn.ExecContext(ctx, "COMMIT")
		if err == nil {
			return nil
		}
		if !isBusy(err) || attempt >= attempts || stopping() {
			break
		}
	}
	rollback(ctx, conn)
	return fmt.Errorf("commit: %w", err)
}

// rollback ends the open transaction on conn. If even that fails, the
// connection is marked bad so database/sql closes it instead of pooling it.
func rollback(ctx context.Context, conn *sql.Conn) {
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// isBusy reports whether err is SQLite's "database is locked".
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// insertTx assigns c.ID and inserts the row within the caller's write
// transaction, so the uniqueness check and the insert cannot race another
// writer.
func insertTx(ctx context.Context, conn *sql.Conn, c *Change) error {
	for attempt := 0; ; attempt++ {
		id := makeID(c.Path, c.TS, idKey(c), attempt)
		if c.Kind == KindDigest {
			id = randomID()
		}
		var one int
		err := conn.QueryRowContext(ctx, `SELECT 1 FROM changes WHERE id = ?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			c.ID = id
			break
		}
		if err != nil {
			return fmt.Errorf("check id: %w", err)
		}
	}
	if c.Kind == "" {
		c.Kind = KindFile
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO changes (id, ts, path, blob, size, mode, uid, gid, origin, intent, kind, target)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.TS, c.Path, c.Blob, c.Size, uint32(c.Mode), c.UID, c.GID, c.Origin, c.Intent, c.Kind, c.Target)
	if err != nil {
		return fmt.Errorf("record change: %w", err)
	}
	return nil
}

func (s *Store) blobPath(sha string) string {
	return filepath.Join(s.dir, "objects", sha[:2], sha)
}

func (s *Store) putBlob(sha string, data []byte) error {
	if s.readOnly != "" {
		return s.readOnlyErr()
	}
	p := s.blobPath(sha)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create object dir: %w", err)
	}
	if err := fsutil.WriteAtomic(p, data, 0o600, os.Geteuid(), os.Getegid()); err != nil {
		return fmt.Errorf("store blob: %w", err)
	}
	return nil
}

// IntegrityCheck runs SQLite's integrity_check and returns nil when the
// database is sound.
func (s *Store) IntegrityCheck() error {
	var res string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&res); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("integrity check: %s", res)
	}
	return nil
}

// HasObject reports whether content sha is already stored, so recording
// it needs no new object (the watcher's free-space floor).
func (s *Store) HasObject(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	_, err := os.Stat(s.blobPath(sha))
	return err == nil
}

// Blob returns the stored bytes for sha, verifying their checksum.
func (s *Store) Blob(sha string) ([]byte, error) {
	if len(sha) != 64 {
		return nil, fmt.Errorf("invalid blob name %q", sha)
	}
	data, err := os.ReadFile(s.blobPath(sha))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", sha[:12], err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("blob %s is corrupt (checksum mismatch)", sha[:12])
	}
	return data, nil
}

const cols = `id, ts, path, blob, size, mode, uid, gid, origin, intent, kind, target`

func scanChanges(rows *sql.Rows) ([]Change, error) {
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var mode uint32
		if err := rows.Scan(&c.ID, &c.TS, &c.Path, &c.Blob, &c.Size, &mode,
			&c.UID, &c.GID, &c.Origin, &c.Intent, &c.Kind, &c.Target); err != nil {
			return nil, fmt.Errorf("read change: %w", err)
		}
		c.Mode = os.FileMode(mode)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read changes: %w", err)
	}
	return out, nil
}

// List returns up to n changes, newest (last inserted) first, whatever their
// timestamps say (see latestTx). An empty path lists all files; n <= 0 means
// no limit.
func (s *Store) List(path string, n int) ([]Change, error) {
	q := `SELECT ` + cols + ` FROM changes`
	var args []any
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve path: %w", err)
		}
		q += ` WHERE path = ?`
		args = append(args, abs)
	}
	q += ` ORDER BY rowid DESC`
	if n > 0 {
		q += ` LIMIT ?`
		args = append(args, n)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list changes: %w", err)
	}
	return scanChanges(rows)
}

// Get resolves a unique id prefix to its change.
func (s *Store) Get(prefix string) (Change, error) {
	p := strings.ToLower(prefix)
	if p == "" || len(p) > idLen || strings.Trim(p, "0123456789abcdef") != "" {
		return Change{}, fmt.Errorf("invalid id %q: expected up to %d hex characters", prefix, idLen)
	}
	rows, err := s.db.Query(`SELECT `+cols+` FROM changes WHERE id LIKE ? ORDER BY id LIMIT 11`, p+"%")
	if err != nil {
		return Change{}, fmt.Errorf("look up id: %w", err)
	}
	cs, err := scanChanges(rows)
	if err != nil {
		return Change{}, err
	}
	switch len(cs) {
	case 0:
		return Change{}, fmt.Errorf("no snapshot with id %q", prefix)
	case 1:
		return cs[0], nil
	}
	ids := make([]string, 0, len(cs))
	for i, c := range cs {
		if i == 10 {
			ids = append(ids, "...")
			break
		}
		ids = append(ids, c.ID)
	}
	return Change{}, fmt.Errorf("ambiguous id %q matches %s", prefix, strings.Join(ids, ", "))
}

// errChanged means a file changed between being read and the write lock
// being taken; the caller reads it again.
var errChanged = errors.New("changed while being recorded")

// maxAttempts bounds how often Snapshot and Restore start over because the
// file kept changing under them.
const maxAttempts = 3

// retryable reports whether an operation should start over because the file
// changed under it: after the read (errChanged) or during it (ErrReplaced).
func retryable(err error) bool {
	return errors.Is(err, errChanged) || errors.Is(err, fsutil.ErrReplaced)
}

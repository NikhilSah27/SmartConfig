// Package store keeps file snapshots: content-addressed blobs under objects/
// and one SQLite row per recorded change.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"smartconfig/internal/fsutil"
)

// DefaultHome is used when $SC_HOME is unset.
const DefaultHome = "/var/lib/smartconfig"

// Origins of a change row.
const (
	OriginManual     = "manual"
	OriginPreRestore = "pre-restore"
	OriginRestore    = "restore"
)

const idLen = 6

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
}

// busyTimeoutMS is how long one statement waits for another connection's
// lock before failing with SQLITE_BUSY. Tests shorten it.
var busyTimeoutMS = 5000

// commitAttempts is how many times writeTx tries COMMIT while other
// connections are busy. Each attempt waits up to busyTimeoutMS.
const commitAttempts = 6

// testHookAfterRestoreWrite, if set by a test, runs in Restore after the file
// is in place and before the restore row is committed.
var testHookAfterRestoreWrite func()

// Store is an open SmartConfig data directory.
type Store struct {
	dir string
	db  *sql.DB
	now func() time.Time
}

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
	dsn := "file:" + filepath.Join(dir, "changes.db") +
		"?_pragma=busy_timeout(" + strconv.Itoa(busyTimeoutMS) + ")&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema in %s: %w", dir, err)
	}
	return &Store{dir: dir, db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Snapshot records the current content of path. For a manual snapshot whose
// content equals the newest recorded one, nothing is inserted and that row is
// returned with unchanged=true. Other origins always insert a row.
func (s *Store) Snapshot(path, origin, intent string) (c Change, unchanged bool, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return Change{}, false, fmt.Errorf("resolve path: %w", err)
	}
	data, meta, err := fsutil.ReadWithMeta(path)
	if err != nil {
		return Change{}, false, err
	}
	sum := sha256.Sum256(data)
	blob := hex.EncodeToString(sum[:])

	if origin == OriginManual {
		last, err := s.latest(path)
		if err != nil {
			return Change{}, false, err
		}
		if last != nil && last.Blob == blob {
			return *last, true, nil
		}
	}
	if err := s.putBlob(blob, data); err != nil {
		return Change{}, false, err
	}
	c = Change{
		TS: s.now().Unix(), Path: path, Blob: blob, Size: int64(len(data)),
		Mode: meta.Mode, UID: meta.UID, GID: meta.GID, Origin: origin, Intent: intent,
	}
	if err := s.insert(&c, nil); err != nil {
		return Change{}, false, err
	}
	return c, false, nil
}

func (s *Store) latest(path string) (*Change, error) {
	cs, err := s.List(path, 1)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// makeID derives an id from path, timestamp and blob. If that id is taken,
// attempt n>0 appends "\n<n>" to the hashed text.
func makeID(path string, ts int64, blob string, attempt int) string {
	text := path + "\n" + strconv.FormatInt(ts, 10) + "\n" + blob
	if attempt > 0 {
		text += "\n" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:idLen]
}

// writeTx runs fn inside one write transaction (BEGIN IMMEDIATE, so the
// database write lock is held from the start) on a connection of its own.
// If fn fails, the transaction is rolled back and fn's error is returned as
// is. COMMIT is retried while other connections keep it busy, because fn may
// already have changed files on disk. A connection whose transaction cannot
// be ended is discarded, never handed back to the pool still holding the
// lock (database/sql's Tx does not guarantee that after a failed COMMIT).
func (s *Store) writeTx(fn func(ctx context.Context, conn *sql.Conn) error) error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("database connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(ctx, conn); err != nil {
		rollback(ctx, conn)
		return err
	}
	for attempt := 1; ; attempt++ {
		_, err = conn.ExecContext(ctx, "COMMIT")
		if err == nil {
			return nil
		}
		if !isBusy(err) || attempt == commitAttempts {
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
		id := makeID(c.Path, c.TS, c.Blob, attempt)
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
	_, err := conn.ExecContext(ctx, `INSERT INTO changes (id, ts, path, blob, size, mode, uid, gid, origin, intent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.TS, c.Path, c.Blob, c.Size, uint32(c.Mode), c.UID, c.GID, c.Origin, c.Intent)
	if err != nil {
		return fmt.Errorf("record change: %w", err)
	}
	return nil
}

// insert records c in its own write transaction. If during is not nil it
// runs after the row is inserted and before the commit, while the write lock
// is held; if it fails, the row is rolled back and its error is returned as
// is.
func (s *Store) insert(c *Change, during func() error) error {
	return s.writeTx(func(ctx context.Context, conn *sql.Conn) error {
		if err := insertTx(ctx, conn, c); err != nil {
			return err
		}
		if during != nil {
			return during()
		}
		return nil
	})
}

func (s *Store) blobPath(sha string) string {
	return filepath.Join(s.dir, "objects", sha[:2], sha)
}

func (s *Store) putBlob(sha string, data []byte) error {
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

const cols = `id, ts, path, blob, size, mode, uid, gid, origin, intent`

func scanChanges(rows *sql.Rows) ([]Change, error) {
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var mode uint32
		if err := rows.Scan(&c.ID, &c.TS, &c.Path, &c.Blob, &c.Size, &mode,
			&c.UID, &c.GID, &c.Origin, &c.Intent); err != nil {
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

// List returns up to n changes, newest first. An empty path lists all files;
// n <= 0 means no limit.
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
	q += ` ORDER BY ts DESC, rowid DESC`
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

// Restore writes snapshot id back to its path with its recorded mode and
// owner. The current file, if any, is first saved as a pre-restore change,
// returned as prev (nil when the file did not exist).
func (s *Store) Restore(id string) (restored Change, prev *Change, err error) {
	src, err := s.Get(id)
	if err != nil {
		return Change{}, nil, err
	}
	data, err := s.Blob(src.Blob)
	if err != nil {
		return Change{}, nil, err
	}
	pre, _, err := s.Snapshot(src.Path, OriginPreRestore, "before restoring "+src.ID)
	switch {
	case err == nil:
		prev = &pre
	case fsutil.IsNotExist(err):
	default:
		return Change{}, nil, fmt.Errorf("save current state: %w", err)
	}
	restored = Change{
		TS: s.now().Unix(), Path: src.Path, Blob: src.Blob, Size: src.Size,
		Mode: src.Mode, UID: src.UID, GID: src.GID,
		Origin: OriginRestore, Intent: "restored from " + src.ID,
	}
	// The file is renamed into place while the restore row is inserted but
	// not yet committed, with the database write lock held. Anyone who sees
	// the rename and then takes the write lock to record it (the M2 watcher)
	// waits for this commit and finds the restore row, instead of logging an
	// unexplained change. If the write fails, the row is rolled back.
	wrote := false
	err = s.insert(&restored, func() error {
		if err := fsutil.WriteAtomic(src.Path, data, src.Mode, src.UID, src.GID); err != nil {
			return fmt.Errorf("restore %s: %w", src.Path, err)
		}
		wrote = true
		if testHookAfterRestoreWrite != nil {
			testHookAfterRestoreWrite()
		}
		return nil
	})
	if err != nil {
		if wrote {
			return Change{}, prev, fmt.Errorf("file restored but not recorded: %w", err)
		}
		return Change{}, prev, err
	}
	return restored, prev, nil
}

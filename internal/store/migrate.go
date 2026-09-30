package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"smartconfig/internal/fsutil"
)

// schemaVersion is the store schema this sc writes (PRAGMA user_version).
// Version 0 is M1's schema: M1 stores and fresh database files both start
// there and take the same migrations.
const schemaVersion = 1

// migrations[i] leaves the store at version i. A version 0 store may be an
// M1 store or a fresh file, so it gets migration 0 (M1's schema, a no-op on
// an M1 store) as well as the ones after it.
var migrations = []string{
	// 0: M1's schema, a no-op on an M1 store.
	schema,
	// 1: row kinds (M2). Existing rows become kind 'file' through the
	// defaults; nothing is rewritten. changes_path_seq (path plus the
	// implicit rowid) finds the newest row of a path without a sort.
	`ALTER TABLE changes ADD COLUMN kind TEXT NOT NULL DEFAULT 'file';
ALTER TABLE changes ADD COLUMN target TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS changes_path_seq ON changes(path);`,
}

// m1Backup is the copy of an M1 database taken before its first migration.
const m1Backup = "changes.db.m1-backup"

// testHookMigrate, if set by a test, runs after migration i and can fail
// the migration.
var testHookMigrate func(v int) error

// testHookBeforeMigrateLock, if set by a test, runs in migrate after the
// schema version is read without the lock and before the write lock is
// taken.
var testHookBeforeMigrateLock func()

// migrate brings the store to schemaVersion. Everything happens in one
// write transaction: concurrent first opens take turns, and a failure
// leaves the store exactly as it was. A store written by a newer sc, or one
// switched to WAL, is refused.
func (s *Store) migrate() error {
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("store %s: read journal mode: %w", s.dir, err)
	}
	// A WAL database cannot be read from a read-only root, which the
	// rescue path (M4) needs, so sc keeps SQLite's rollback journal.
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("store %s uses journal mode %s, sc needs delete", s.dir, mode)
	}
	// An up-to-date store needs no lock, so read-only commands never wait
	// for a writer and work where the store cannot be written.
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store %s: read schema version: %w", s.dir, err)
	}
	if v == schemaVersion {
		return nil
	}
	if v > schemaVersion {
		return fmt.Errorf("store %s was written by a newer sc (schema %d)", s.dir, v)
	}
	if testHookBeforeMigrateLock != nil {
		testHookBeforeMigrateLock()
	}
	var stepErr error
	err := s.writeTx(false, func(ctx context.Context, conn *sql.Conn) error {
		stepErr = s.migrateTx(ctx, conn)
		return stepErr
	})
	if err != nil && err != stepErr { // BEGIN or COMMIT failed
		return fmt.Errorf("store %s: migrate to schema %d: %w", s.dir, schemaVersion, err)
	}
	return err
}

// migrateTx is migrate's work inside the write transaction.
func (s *Store) migrateTx(ctx context.Context, conn *sql.Conn) error {
	// Read again under the lock: another process may have migrated the
	// store meanwhile.
	var v int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store %s: read schema version: %w", s.dir, err)
	}
	if v == schemaVersion {
		return nil
	}
	if v > schemaVersion {
		return fmt.Errorf("store %s was written by a newer sc (schema %d)", s.dir, v)
	}
	if v == 0 {
		if err := backupM1(ctx, conn, s.dir); err != nil {
			return err
		}
	}
	from := v + 1
	if v == 0 {
		from = 0
	}
	for i := from; i <= schemaVersion; i++ {
		if _, err := conn.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("store %s: migrate to schema %d: %w", s.dir, i, err)
		}
		if testHookMigrate != nil {
			if err := testHookMigrate(i); err != nil {
				return err
			}
		}
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("store %s: set schema version: %w", s.dir, err)
	}
	return nil
}

// backupM1 copies an M1 database that holds rows to changes.db.m1-backup
// (0600, synced, renamed into place) before its first migration. It runs
// inside the migration's write transaction, so no writer can change the
// database meanwhile. An existing backup is kept: it is the copy from before
// any migration (a failed migration leaves the database as it was anyway).
//
// The copy is read through conn (sqlite3_serialize), never by opening
// changes.db: SQLite's locks are POSIX locks, which the kernel drops for the
// whole process when any descriptor on the file is closed. Other processes
// could then write while this one migrates, and roll its journal back.
func backupM1(ctx context.Context, conn *sql.Conn, dir string) error {
	var tables int
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'changes'`).Scan(&tables); err != nil {
		return fmt.Errorf("store %s: read schema: %w", dir, err)
	}
	if tables == 0 {
		return nil // a fresh database file
	}
	var rows bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM changes)`).Scan(&rows); err != nil {
		return fmt.Errorf("store %s: read changes: %w", dir, err)
	}
	if !rows {
		return nil
	}
	dst := filepath.Join(dir, m1Backup)
	if fi, err := os.Lstat(dst); err == nil {
		// Only a real earlier copy counts: a symlink (even a dangling one)
		// or anything else there would leave the store with no backup.
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("store %s: %s is not a regular file, move it away", dir, m1Backup)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store %s: %w", dir, err)
	}
	var data []byte
	err := conn.Raw(func(dc any) error {
		ser, ok := dc.(interface{ Serialize() ([]byte, error) })
		if !ok {
			return errors.New("the SQLite driver cannot serialize")
		}
		var err error
		data, err = ser.Serialize()
		return err
	})
	if err != nil {
		return fmt.Errorf("store %s: back up the M1 database: %w", dir, err)
	}
	if err := fsutil.WriteAtomic(dst, data, 0o600, os.Getuid(), os.Getgid()); err != nil {
		return fmt.Errorf("store %s: back up the M1 database: %w", dir, err)
	}
	return nil
}

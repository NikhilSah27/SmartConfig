package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PruneResult says what Prune deleted, or, run dry, would delete.
type PruneResult struct {
	Rows  int   // rows
	Blobs int   // blob files no row uses any more
	Bytes int64 // the blobs' size
}

// BlobGrace is how new a blob may be and still be kept though no row uses
// it: a writer puts its blob before it takes the write lock, and a writer
// of a release before the M5 follow-ups does not look for it again under
// the lock (M5 follow-up 2).
const BlobGrace = time.Hour

// errDryRun rolls back a dry run's transaction.
var errDryRun = errors.New("dry run")

// Prune deletes the automatic rows recorded before cutoff (M5 follow-up 2),
// except, for each path:
//   - its newest row;
//   - for each line in lines (rowids: the boots file's verdicts), its
//     newest row at or before that line, the version sc status compares
//     with.
//
// Manual, pre-restore, restore and edit rows are never deleted. Then it
// deletes the blobs that no row uses and that are older than BlobGrace.
//
// The rows go in one transaction, which commits before any blob is
// touched, so a crash can leave a blob no row uses but never a row without
// its blob. The blobs go under the write lock, where every writer checks
// its blob again (Record, commitWrite). With dryRun nothing changes, and
// the result is what a real run would do now.
//
// There is no VACUUM. It could renumber the rowids, which order the rows
// and which the boots file holds. The newest row is never deleted, so a
// new row's rowid is still past every old one.
func (s *Store) Prune(cutoff time.Time, lines []int64, dryRun bool) (PruneResult, error) {
	var res PruneResult
	if s.readOnly != "" {
		return res, s.readOnlyErr()
	}
	lines = sortedLines(lines)
	err := s.writeTx(false, func(ctx context.Context, conn *sql.Conn) error {
		doomed, err := prunable(ctx, conn, cutoff.Unix(), lines)
		if err != nil {
			return err
		}
		for len(doomed) > 0 {
			n := min(len(doomed), 500)
			args := make([]any, n)
			for i, id := range doomed[:n] {
				args[i] = id
			}
			q := "DELETE FROM changes WHERE rowid IN (?" + strings.Repeat(",?", n-1) + ")"
			r, err := conn.ExecContext(ctx, q, args...)
			if err != nil {
				return fmt.Errorf("prune: %w", err)
			}
			k, _ := r.RowsAffected()
			res.Rows += int(k)
			doomed = doomed[n:]
		}
		if dryRun {
			// The blobs a real run would delete, with these rows gone.
			res.Blobs, res.Bytes, err = s.pruneBlobs(ctx, conn, true)
			if err != nil {
				return err
			}
			return errDryRun
		}
		return nil
	})
	if dryRun && errors.Is(err, errDryRun) {
		return res, nil
	}
	if err != nil {
		return PruneResult{}, err
	}
	err = s.writeTx(false, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		res.Blobs, res.Bytes, err = s.pruneBlobs(ctx, conn, false)
		return err
	})
	return res, err
}

// sortedLines is lines ascending, without duplicates and rowids below 1
// (a verdict given while the store could not be read has -1).
func sortedLines(lines []int64) []int64 {
	var out []int64
	for _, l := range lines {
		if l > 0 {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for i, l := range out {
		if i == 0 || l != out[n-1] {
			out[n] = l
			n++
		}
	}
	return out[:n]
}

// prunable returns the rowids Prune deletes: a path's rows in order, each
// one but the last judged with the rowid of the row after it.
func prunable(ctx context.Context, conn *sql.Conn, before int64, lines []int64) ([]int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT rowid, path, origin, ts FROM changes ORDER BY path, rowid`)
	if err != nil {
		return nil, fmt.Errorf("prune: %w", err)
	}
	defer rows.Close()
	type row struct {
		id     int64
		path   string
		origin string
		ts     int64
	}
	// kept reports whether a line falls at or after r and before the next
	// row of its path: then r is that path's version at the line.
	kept := func(r, next int64) bool {
		i := sort.Search(len(lines), func(i int) bool { return lines[i] >= r })
		return i < len(lines) && lines[i] < next
	}
	var doomed []int64
	var prev *row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path, &r.origin, &r.ts); err != nil {
			return nil, fmt.Errorf("prune: %w", err)
		}
		if prev != nil && prev.path == r.path &&
			prev.origin == OriginAuto && prev.ts < before && !kept(prev.id, r.id) {
			doomed = append(doomed, prev.id)
		}
		prev = &r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("prune: %w", err)
	}
	return doomed, nil
}

// pruneBlobs deletes, or with dry only counts, the blob files no row uses
// that are older than BlobGrace. It runs under the write lock. A name that
// is not a blob's (a temp file of a write) is left alone.
func (s *Store) pruneBlobs(ctx context.Context, conn *sql.Conn, dry bool) (n int, bytes int64, err error) {
	used := map[string]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT blob FROM changes WHERE blob <> ''`)
	if err != nil {
		return 0, 0, fmt.Errorf("prune: %w", err)
	}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("prune: %w", err)
		}
		used[b] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("prune: %w", err)
	}
	old := time.Now().Add(-BlobGrace) // files' times are the wall clock's, not s.now's
	dirs, err := os.ReadDir(filepath.Join(s.dir, "objects"))
	if err != nil {
		return 0, 0, fmt.Errorf("prune: %w", err)
	}
	for _, d := range dirs {
		if !d.IsDir() || len(d.Name()) != 2 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.dir, "objects", d.Name()))
		if err != nil {
			return n, bytes, fmt.Errorf("prune: %w", err)
		}
		for _, f := range files {
			name := f.Name()
			if !isBlobName(name) || name[:2] != d.Name() || used[name] {
				continue
			}
			fi, err := f.Info()
			if err != nil || !fi.Mode().IsRegular() || !fi.ModTime().Before(old) {
				continue
			}
			if !dry {
				if err := os.Remove(s.blobPath(name)); err != nil {
					return n, bytes, fmt.Errorf("prune: %w", err)
				}
			}
			n++
			bytes += fi.Size()
		}
	}
	return n, bytes, nil
}

// isBlobName reports whether name is a sha256 in lower-case hex.
func isBlobName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, c := range name {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PruneResult says what Prune deleted, or, run dry, would delete.
type PruneResult struct {
	Rows    int   // rows
	Blobs   int   // stored versions (and a killed writer's leftovers) no row uses
	Bytes   int64 // their size
	Skipped int   // blobs that could not be removed
	SkipErr error // the first such error
}

// BlobGrace is how new a blob may be and still be kept though no row uses
// it: a writer puts its blob before it takes the write lock. A writer of
// a release before the M5 follow-ups does not look for its blob again
// under the lock, and the grace covers only a blob it has just written:
// sc prune refuses while scd runs such a release (M5 follow-up 2, the
// chunk I review, I4).
const BlobGrace = time.Hour

// Batches keep each write transaction short: scd, sc restore and sc edit
// wait at most 5 s for the write lock (the chunk I review, I5).
var (
	pruneRowBatch  = 1000
	pruneBlobBatch = 500
	prunePlanPage  = 5000 // tests make them small
)

// testHookPruneBlobs, if set by a test, runs in Prune after the rows are
// deleted and before the blobs are.
var testHookPruneBlobs func()

// Prune deletes automatic rows (M5 follow-up 2): versions recorded by scd
// that a newer row replaced before cutoff, so that the version stopped
// being current before it (the chunk I review, I1). For each path it
// always keeps:
//   - its newest row;
//   - its first row, which sc status compares with for a path first seen
//     after the last healthy boot (proof that a file is new, I2);
//   - for each line in lines (rowids: the boots file's verdicts), its
//     newest row at or before that line, the version sc status compares
//     with.
//
// Manual, pre-restore, restore and edit rows are never deleted. Then it
// deletes the blobs no row uses that are older than BlobGrace, with a
// killed writer's leftover temp files.
//
// The plan is read in pages with no lock. Rows are deleted in short write
// transactions, all of them before any blob is touched, so a crash can
// leave a blob no row uses but never a row without its blob. Blobs are
// deleted in short batches under the write lock, where every writer puts
// its blob again (Record, commitWrite), and each batch first adds the
// blobs of rows newer than the plan.
//
// After Interrupt it stops between batches and returns ErrInterrupted
// with what it did. With dryRun nothing changes, and the result is what a
// real run would do now.
//
// There is no VACUUM. It could renumber the rowids, which order the rows
// and which the boots file holds. The newest row is never deleted, so a
// new row's rowid is still past every old one.
func (s *Store) Prune(cutoff time.Time, lines []int64, dryRun bool) (PruneResult, error) {
	var res PruneResult
	if s.readOnly != "" {
		return res, s.readOnlyErr()
	}
	doomed, used, top, err := s.prunePlan(cutoff.Unix(), sortedLines(lines))
	if err != nil {
		return res, err
	}
	if dryRun {
		res.Rows = len(doomed)
		err := s.pruneBlobs(used, top, true, &res)
		return res, err
	}
	for len(doomed) > 0 {
		if stopping() {
			return res, ErrInterrupted
		}
		n := min(len(doomed), pruneRowBatch)
		args := make([]any, n)
		for i, id := range doomed[:n] {
			args[i] = id
		}
		err := s.writeTx(false, func(ctx context.Context, conn *sql.Conn) error {
			r, err := conn.ExecContext(ctx, "DELETE FROM changes WHERE rowid IN (?"+strings.Repeat(",?", n-1)+")", args...)
			if err != nil {
				return fmt.Errorf("prune: %w", err)
			}
			k, _ := r.RowsAffected()
			res.Rows += int(k)
			return nil
		})
		if err != nil {
			return res, err
		}
		doomed = doomed[n:]
	}
	// Only now, every planned row gone: used is what the rows left use.
	if testHookPruneBlobs != nil {
		testHookPruneBlobs()
	}
	err = s.pruneBlobs(used, top, false, &res)
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

// prunePlan reads the rows up to the newest at its start (top), a page at
// a time with no lock, and returns the rowids Prune deletes and the blobs
// the other rows use. Rows newer than top are not planned: their paths'
// rows before them are judged as if top were the end, which only keeps
// more, and their blobs are added under the lock (pruneBlobs).
func (s *Store) prunePlan(before int64, lines []int64) (doomed []int64, used map[string]bool, top int64, err error) {
	if err := s.db.QueryRow(`SELECT coalesce(max(rowid), 0) FROM changes`).Scan(&top); err != nil {
		return nil, nil, 0, fmt.Errorf("prune: %w", err)
	}
	type row struct {
		id           int64
		path, origin string
		ts           int64
		blob         string
	}
	// kept reports whether a line falls at or after r and before the next
	// row of its path: then r is that path's version at the line.
	kept := func(r, next int64) bool {
		i := sort.Search(len(lines), func(i int) bool { return lines[i] >= r })
		return i < len(lines) && lines[i] < next
	}
	used = map[string]bool{}
	var prev *row
	first := false // prev is its path's first row
	judge := func(next *row) {
		// prev, given the row after it (nil: prev is its path's newest).
		if next != nil && next.path == prev.path && !first &&
			prev.origin == OriginAuto && prev.ts < before && next.ts < before && !kept(prev.id, next.id) {
			doomed = append(doomed, prev.id)
		} else if prev.blob != "" {
			used[prev.blob] = true
		}
	}
	lastPath, lastID := "", int64(0)
	for {
		rows, err := s.db.Query(`SELECT rowid, path, origin, ts, blob FROM changes
			WHERE rowid <= ? AND (path, rowid) > (?, ?) ORDER BY path, rowid LIMIT ?`, top, lastPath, lastID, prunePlanPage)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("prune: %w", err)
		}
		n := 0
		for rows.Next() {
			r := &row{}
			if err := rows.Scan(&r.id, &r.path, &r.origin, &r.ts, &r.blob); err != nil {
				rows.Close()
				return nil, nil, 0, fmt.Errorf("prune: %w", err)
			}
			n++
			if prev != nil {
				judge(r)
			}
			first = prev == nil || prev.path != r.path
			prev = r
			lastPath, lastID = r.path, r.id
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, 0, fmt.Errorf("prune: %w", err)
		}
		if n < prunePlanPage {
			break
		}
	}
	if prev != nil {
		judge(nil)
	}
	return doomed, used, top, nil
}

// pruneBlobs deletes, or with dry only counts, the files in objects/ no
// row uses that are older than BlobGrace: blobs, and a killed writer's
// temp files (".<sha>.sc-tmp-*"). used holds the blobs of the rows up to
// top that stay. Each batch of deletions runs under the write lock and
// first adds the blobs of rows newer than top. A file that cannot be
// removed is skipped and counted.
func (s *Store) pruneBlobs(used map[string]bool, top int64, dry bool, res *PruneResult) error {
	old := time.Now().Add(-BlobGrace) // files' times are the wall clock's, not s.now's
	type cand struct {
		path, blob string // blob is "" for a temp file
		size       int64
	}
	var cands []cand
	objects := filepath.Join(s.dir, "objects")
	dirs, err := os.ReadDir(objects)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	for _, d := range dirs {
		if !d.IsDir() || len(d.Name()) != 2 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(objects, d.Name()))
		if err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		for _, f := range files {
			name, blob := f.Name(), ""
			switch {
			case isBlobName(name) && name[:2] == d.Name():
				blob = name
			case strings.HasPrefix(name, ".") && strings.Contains(name, ".sc-tmp-") && isBlobName(strings.TrimPrefix(name[:strings.Index(name, ".sc-tmp-")], ".")):
				// a temp file of putBlob's write, left by a killed writer
			default:
				continue
			}
			if blob != "" && used[blob] {
				continue
			}
			fi, err := f.Info()
			if err != nil || !fi.Mode().IsRegular() || !fi.ModTime().Before(old) {
				continue
			}
			cands = append(cands, cand{filepath.Join(objects, d.Name(), name), blob, fi.Size()})
		}
	}
	newer := func(q func(string, ...any) (*sql.Rows, error)) error {
		rows, err := q(`SELECT rowid, blob FROM changes WHERE rowid > ?`, top)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var b string
			if err := rows.Scan(&id, &b); err != nil {
				return err
			}
			used[b], top = true, max(top, id)
		}
		return rows.Err()
	}
	if dry {
		if err := newer(s.db.Query); err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		for _, c := range cands {
			if c.blob == "" || !used[c.blob] {
				res.Blobs, res.Bytes = res.Blobs+1, res.Bytes+c.size
			}
		}
		return nil
	}
	for len(cands) > 0 {
		if stopping() {
			return ErrInterrupted
		}
		batch := cands[:min(len(cands), pruneBlobBatch)]
		cands = cands[len(batch):]
		err := s.writeTx(false, func(ctx context.Context, conn *sql.Conn) error {
			if err := newer(func(q string, args ...any) (*sql.Rows, error) { return conn.QueryContext(ctx, q, args...) }); err != nil {
				return fmt.Errorf("prune: %w", err)
			}
			for _, c := range batch {
				if c.blob != "" && used[c.blob] {
					continue
				}
				if err := os.Remove(c.path); err != nil {
					if !os.IsNotExist(err) {
						res.Skipped++
						if res.SkipErr == nil {
							res.SkipErr = err
						}
					}
					continue
				}
				res.Blobs, res.Bytes = res.Blobs+1, res.Bytes+c.size
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
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

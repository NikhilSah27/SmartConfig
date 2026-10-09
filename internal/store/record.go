package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"smartconfig/internal/fsutil"
)

// Row kinds (plan 5.2).
const (
	KindFile    = "file"    // content in objects/
	KindLink    = "link"    // symlink: target text, no blob
	KindDigest  = "digest"  // fingerprint only: sha256, mode, owner; no object
	KindDeleted = "deleted" // the path was absent
)

// OriginAuto marks rows written by the watcher.
const OriginAuto = "auto"

// MaxBatch is the most observations one Record call takes.
const MaxBatch = 50

// Obs is one observation of a path for Record.
type Obs struct {
	Path       string
	State      *fsutil.State // nil: the path is absent
	Digest     bool          // fingerprint only, even if the rule says no
	Created    bool          // proof of absence: write "did not exist" first
	CheckStamp bool          // re-check the path under the write lock
	Force      bool          // insert even if equal to the newest row
	// Explicit: a snapshot the user asked for (sc snapshot). Equal to an
	// automatic row, it is still a row of its own, a manual one, which
	// sc prune keeps (the M5 follow-ups' chunk I review, I3).
	Explicit bool
	Origin   string
	Intent   string // "" for OriginAuto: computed (changed, mode ...)
	Suffix   string // appended to a computed intent (SuffixNotWatching, ...)
}

// Intent suffixes for watcher rows that no live event explained (plan 6.5).
const (
	SuffixNotWatching = " while not watching" // found by the startup rescan
	SuffixRescan      = " (found by rescan)"  // found by a later rescan
	SuffixChanging    = " (still changing)"   // recorded although never stable
	SuffixLimited     = " (rate-limited)"     // waited for its file's row budget
)

// Result is what Record did with one observation.
type Result struct {
	Change   Change // the row inserted, or the newest row if none was
	Recorded bool
	Moved    bool // the path changed after it was read: read it again
}

// testHookRecordInTx, if set by a test, runs in Record after the rows are
// inserted and before the transaction commits.
var testHookRecordInTx func()

// SetFingerprintOnly replaces the rule that decides which paths are
// recorded as fingerprints only (Open sets scope.Default's).
func (s *Store) SetFingerprintOnly(f func(path string) bool) { s.fingerprint = f }

// FingerprintOnly reports whether only path's sha256, mode and owner may
// be stored. The rule is applied to the path as given and to the path with
// its directories resolved, so /proc/self/root/etc/machine-id, or a path
// through a symlink to /, is caught too.
func (s *Store) FingerprintOnly(path string) bool {
	if s.fingerprint(path) {
		return true
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	return err == nil && s.fingerprint(filepath.Join(dir, filepath.Base(path)))
}

// Record stores up to MaxBatch observations (plan 5.4). Observations equal
// to their path's newest row are dropped first, without a lock, unless
// Force is set; file blobs are written next; then one write transaction
// re-reads each newest row, checks the stamp when CheckStamp is set (a
// path that changed gives Moved and no row), inserts a "did not exist" row
// first for a Created path that has no rows, and inserts the row.
func (s *Store) Record(obs []Obs) ([]Result, error) {
	if len(obs) > MaxBatch {
		return nil, fmt.Errorf("record: %d observations, at most %d", len(obs), MaxBatch)
	}
	res := make([]Result, len(obs))
	rows := make([]*Change, len(obs))
	datas := make([][]byte, len(obs))
	var todo []int
	for i, o := range obs {
		c, data, err := s.rowFor(o)
		if err != nil {
			return nil, err
		}
		last, err := s.latest(o.Path)
		if err != nil {
			return nil, err
		}
		if nothingNew(o, last, c) {
			if last != nil {
				res[i].Change = *last
			}
			continue
		}
		if c.Kind == KindFile {
			if err := s.putBlob(c.Blob, data); err != nil {
				return nil, err
			}
		}
		rows[i], datas[i] = c, data
		todo = append(todo, i)
	}
	if len(todo) == 0 {
		return res, nil
	}
	if testHookSnapshotBeforeLock != nil {
		testHookSnapshotBeforeLock()
	}
	err := s.writeTx(false, func(ctx context.Context, conn *sql.Conn) error {
		if stopping() {
			return ErrInterrupted
		}
		ts := s.now().Unix()
		for _, i := range todo {
			o, c := obs[i], rows[i]
			if o.CheckStamp && !stillAsRead(o) {
				res[i] = Result{Moved: true}
				continue
			}
			last, err := latestTx(ctx, conn, o.Path)
			if err != nil {
				return err
			}
			if nothingNew(o, last, c) {
				if last != nil {
					res[i] = Result{Change: *last}
				}
				continue
			}
			if o.Origin == OriginAuto && o.Intent == "" {
				c.Intent = autoIntent(last, c, o.Created)
				// "first seen while not watching" says nothing more.
				if !(c.Intent == "first seen" && o.Suffix == SuffixNotWatching) {
					c.Intent += o.Suffix
				}
			}
			if o.Created && last == nil && c.Kind != KindDeleted {
				gone := Change{Path: o.Path, Kind: KindDeleted, Origin: o.Origin, Intent: "did not exist", TS: ts}
				if err := insertTx(ctx, conn, &gone); err != nil {
					return err
				}
			}
			c.TS = ts
			if c.Kind == KindFile {
				// sc prune may have deleted the blob since it was put,
				// though only under this lock (M5 follow-up 2).
				if err := s.putBlob(c.Blob, datas[i]); err != nil {
					return err
				}
			}
			if err := insertTx(ctx, conn, c); err != nil {
				return err
			}
			res[i] = Result{Change: *c, Recorded: true}
		}
		if testHookRecordInTx != nil {
			testHookRecordInTx()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// rowFor builds the row an observation would insert (without id and ts),
// and the content to store for a file row.
func (s *Store) rowFor(o Obs) (*Change, []byte, error) {
	c := &Change{Path: o.Path, Origin: o.Origin, Intent: o.Intent}
	st := o.State
	switch {
	case st == nil:
		c.Kind = KindDeleted
	case st.Kind == "link":
		c.Kind, c.Target, c.Size = KindLink, st.Target, int64(len(st.Target))
		c.Mode, c.UID, c.GID = st.Meta.Mode, st.Meta.UID, st.Meta.GID
	case st.Kind == "file":
		sum := sha256.Sum256(st.Data)
		c.Kind, c.Blob, c.Size = KindFile, hex.EncodeToString(sum[:]), int64(len(st.Data))
		c.Mode, c.UID, c.GID = st.Meta.Mode, st.Meta.UID, st.Meta.GID
		if o.Digest || s.FingerprintOnly(o.Path) {
			c.Kind = KindDigest
			return c, nil, nil
		}
		return c, st.Data, nil
	default:
		return nil, nil, fmt.Errorf("record %s: unknown kind %q", o.Path, st.Kind)
	}
	return c, nil, nil
}

// nothingNew reports whether observation o, as row c, adds nothing after
// the newest row last: it equals it (unless Force), or it says a path with
// no rows is absent. An explicit snapshot equal to an automatic row is
// new (Obs.Explicit).
func nothingNew(o Obs, last, c *Change) bool {
	if c.Kind == KindDeleted && last == nil {
		return true
	}
	return !o.Force && sameState(last, c) && !(o.Explicit && last.Origin == OriginAuto)
}

// sameState reports whether row c records the same state as the newest row
// last: the dedup key is kind, blob, target, mode and owner.
func sameState(last, c *Change) bool {
	return last != nil && last.Kind == c.Kind && last.Blob == c.Blob && last.Target == c.Target &&
		last.Mode == c.Mode && last.UID == c.UID && last.GID == c.GID
}

// stillAsRead reports whether o's path is still the version it read: the
// same stamp, or still absent.
func stillAsRead(o Obs) bool {
	st, err := fsutil.LstatStamp(o.Path)
	if o.State == nil {
		// A directory on the way that became a file (ENOTDIR) also means
		// the path is gone.
		return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
	}
	return err == nil && st == o.State.Meta.Stamp
}

// autoIntent describes the change from last to c (plan 6.5).
func autoIntent(last, c *Change, created bool) string {
	switch {
	case c.Kind == KindDeleted:
		return "deleted"
	case last == nil && created:
		return "created"
	case last == nil:
		return "first seen"
	case last.Kind == KindDeleted:
		return "created"
	case c.Kind == KindLink && last.Kind != KindLink:
		return "now a link -> " + c.Target
	case c.Kind != KindLink && last.Kind == KindLink:
		return "now a file"
	}
	var parts []string
	switch {
	case c.Kind == KindLink && c.Target != last.Target:
		parts = append(parts, "link -> "+c.Target)
	case c.Kind != KindLink:
		if c.Blob != last.Blob {
			parts = append(parts, "changed")
		}
		switch {
		case c.Kind == KindDigest && last.Kind == KindFile:
			parts = append(parts, "now fingerprint only")
		case c.Kind == KindFile && last.Kind == KindDigest:
			parts = append(parts, "now with content")
		}
	}
	if c.Mode != last.Mode {
		parts = append(parts, fmt.Sprintf("mode %s->%s", octal(last.Mode), octal(c.Mode)))
	}
	if c.UID != last.UID || c.GID != last.GID {
		parts = append(parts, fmt.Sprintf("owner %d:%d->%d:%d", last.UID, last.GID, c.UID, c.GID))
	}
	return strings.Join(parts, ", ")
}

// octal writes a mode as chmod(1) takes it, special bits included.
func octal(m os.FileMode) string {
	n := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		n |= 0o4000
	}
	if m&os.ModeSetgid != 0 {
		n |= 0o2000
	}
	if m&os.ModeSticky != 0 {
		n |= 0o1000
	}
	return fmt.Sprintf("%04o", n)
}

// latest returns the newest row of path outside any transaction.
func (s *Store) latest(path string) (*Change, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM changes WHERE path = ? ORDER BY rowid DESC LIMIT 1`, path)
	if err != nil {
		return nil, fmt.Errorf("read latest change: %w", err)
	}
	cs, err := scanChanges(rows)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// LivePaths returns every path whose newest row is not a deletion, for a
// rescan to look at again.
func (s *Store) LivePaths() ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM changes WHERE rowid IN
		(SELECT max(rowid) FROM changes GROUP BY path) AND kind <> 'deleted' ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("list live paths: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("list live paths: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"smartconfig/internal/fsutil"
)

// ErrFileChanged is Replace's error when the path is no longer the version
// the caller read. Nothing was written.
var ErrFileChanged = errors.New("the file changed on disk meanwhile")

// Replace makes data the content of path, with mode and owner, and records
// it as one row with origin and intent (sc edit, M3 plan 6.2). base is the
// state the caller read and built data from, nil when the path did not
// exist: if the path is no longer that version, nothing is written and the
// error is ErrFileChanged.
//
// The state before is recorded first unless the newest row already holds
// it; it is returned as prev (nil for a new file). The write and its row
// then go through the same commit as a restore: under the write lock, so
// the watcher adds no row of its own. A new file gets a row saying the path
// was absent first ("did not exist", or "deleted" when the path has older
// rows), unless the newest row says so, so restoring that row undoes the
// creation.
//
// Nothing is written before every refusal has passed: a fingerprint-only
// path, a base that is not a regular file, and a directory that is not a
// real directory owned by root or the caller.
func (s *Store) Replace(path string, data []byte, mode os.FileMode, uid, gid int, base *fsutil.State, origin, intent string) (row Change, prev *Change, err error) {
	if err := s.CanReplace(path, base); err != nil {
		return Change{}, nil, err
	}
	if err := TooBig(path, data); err != nil {
		return Change{}, nil, err
	}
	if stopping() {
		return Change{}, nil, fmt.Errorf("write %s: %w (file not changed)", path, ErrInterrupted)
	}
	var stamp fsutil.Stamp
	if base != nil {
		stamp = base.Meta.Stamp
		res, err := s.Record([]Obs{{Path: path, State: base, CheckStamp: true, Origin: OriginManual, Intent: "before sc edit"}})
		if err != nil {
			return Change{}, nil, fmt.Errorf("write %s: save current state: %w (file not changed)", path, err)
		}
		if res[0].Moved {
			return Change{}, nil, fmt.Errorf("write %s: %w (file not changed)", path, ErrFileChanged)
		}
		pre := res[0].Change
		prev = &pre
	}
	sum := sha256.Sum256(data)
	row = Change{Path: path, Kind: KindFile, Blob: hex.EncodeToString(sum[:]), Size: int64(len(data)),
		Mode: mode, UID: uid, GID: gid, Origin: origin, Intent: intent}
	if err := s.putBlob(row.Blob, data); err != nil {
		return Change{}, prev, fmt.Errorf("write %s: %w (file not changed)", path, err)
	}
	// The slow part of the write (temp file, fsync) happens before the lock.
	pending, err := fsutil.PrepareAtomic(path, data, mode, uid, gid)
	if err != nil {
		return Change{}, prev, fmt.Errorf("write %s: %w (file not changed)", path, err)
	}
	defer pending.Discard()
	if testHookBeforeReplaceLock != nil {
		testHookBeforeReplaceLock()
	}
	wrote, err := s.commitWrite(&row, base != nil, stamp, base == nil, pending.Commit)
	switch {
	case err == nil:
		return row, prev, nil
	case errors.Is(err, errChanged):
		return Change{}, prev, fmt.Errorf("write %s: %w (file not changed)", path, ErrFileChanged)
	case wrote:
		return Change{}, prev, fmt.Errorf("file written but not recorded: %w (run: sc snapshot %s)", err, path)
	}
	return Change{}, prev, fmt.Errorf("write %s: %w (file not changed)", path, err)
}

// CanReplace returns Replace's refusal for path and base, or nil: sc edit
// asks before it opens the editor, so nobody edits a file sc will not save.
func (s *Store) CanReplace(path string, base *fsutil.State) error {
	if s.readOnly != "" {
		return s.readOnlyErr()
	}
	if s.FingerprintOnly(path) {
		return fmt.Errorf("%s is fingerprint-only; sc never writes it", path)
	}
	if base != nil && base.Kind != "file" {
		return fmt.Errorf("%s is a symlink; sc writes regular files only", path)
	}
	return safeDirFor(path, "write", "")
}

// TooBig returns the refusal for content sc would not read back: more than
// fsutil.MaxSize bytes.
func TooBig(path string, data []byte) error {
	if len(data) > fsutil.MaxSize {
		return fmt.Errorf("write %s: %d bytes is larger than the %d MB limit (file not changed)", path, len(data), fsutil.MaxSize>>20)
	}
	return nil
}

// testHookBeforeReplaceLock, if set by a test, runs in Replace after the
// state before is saved and before the write lock is taken.
var testHookBeforeReplaceLock func()

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"smartconfig/internal/fsutil"
)

// Restore puts path back as row id records it: a file with its content,
// mode and owner, a link with its target and owner, or absent (a deleted or
// "did not exist" row removes the file). Whatever is there now is first
// saved and committed as a pre-restore row, returned as prev (nil when the
// path was absent), so the restore can be undone even if recording it
// fails. A restore of a deletion whose path is already absent does nothing
// and returns a restored row with an empty ID.
//
// Nothing is written before every refusal has passed (plan 5.6): a
// fingerprint-only path, a digest row, and a target directory that is not
// a real directory owned by root or the caller.
func (s *Store) Restore(id string) (restored Change, prev *Change, err error) {
	src, err := s.Get(id)
	if err != nil {
		return Change{}, nil, err
	}
	if s.FingerprintOnly(src.Path) {
		return Change{}, nil, fmt.Errorf("%s is fingerprint-only; sc never restores it", src.Path)
	}
	if src.Kind == KindDigest {
		return Change{}, nil, fmt.Errorf("%s keeps only a fingerprint of %s, not its content", src.ID, src.Path)
	}
	if src.Kind == KindDeleted {
		if _, err := os.Lstat(src.Path); errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return Change{}, nil, nil // already absent, as the row says
		}
	}
	if err := safeDir(src); err != nil {
		return Change{}, nil, err
	}
	var pending *fsutil.Pending
	if src.Kind == KindFile {
		data, err := s.Blob(src.Blob)
		if err != nil {
			return Change{}, nil, err
		}
		if stopping() {
			return Change{}, nil, fmt.Errorf("restore %s: %w (file not changed)", src.Path, ErrInterrupted)
		}
		// The slow part of the write (temp file, fsync) happens before any
		// lock, so a slow disk does not hold up other sc commands.
		pending, err = fsutil.PrepareAtomic(src.Path, data, src.Mode, src.UID, src.GID)
		if err != nil {
			return Change{}, nil, fmt.Errorf("restore %s: %w (file not changed)", src.Path, err)
		}
		defer pending.Discard()
	}

	if testHookBeforePreRestore != nil {
		testHookBeforePreRestore()
	}
	wrote := false
	for attempt := 1; ; attempt++ {
		if stopping() {
			err = ErrInterrupted
			break
		}
		var stamp fsutil.Stamp
		prev, stamp, err = s.savePreRestore(src)
		if retryable(err) && attempt < maxAttempts {
			continue
		}
		if err != nil {
			if retryable(err) {
				break
			}
			return Change{}, nil, fmt.Errorf("restore %s: save current state: %w (file not changed)", src.Path, err)
		}
		if src.Kind == KindDeleted && prev == nil {
			return Change{}, nil, nil // gone meanwhile
		}
		if testHookBeforeRestoreLock != nil {
			testHookBeforeRestoreLock()
		}
		restored, wrote, err = s.commitRestore(src, prev != nil, stamp, pending)
		if !retryable(err) || attempt == maxAttempts {
			break
		}
	}
	what := map[string]string{KindLink: "link restored", KindDeleted: "file removed"}[src.Kind]
	if what == "" {
		what = "file restored"
	}
	switch {
	case err == nil:
		return restored, prev, nil
	case retryable(err):
		return Change{}, prev, fmt.Errorf("restore %s: the file kept changing, try again (file not changed)", src.Path)
	case wrote && prev != nil:
		return Change{}, prev, fmt.Errorf("%s but not recorded: %w; previous content saved as %s (run: sc snapshot %s)", what, err, prev.ID, src.Path)
	case wrote:
		return Change{}, prev, fmt.Errorf("%s but not recorded: %w (run: sc snapshot %s)", what, err, src.Path)
	}
	return Change{}, prev, fmt.Errorf("restore %s: %w (file not changed)", src.Path, err)
}

// testHookBeforePreRestore, if set by a test, runs in Restore after the
// refusals and before the current state is read.
var testHookBeforePreRestore func()

// safeDir checks every directory from / down to the parent of src.Path
// with lstat: each must be a real directory, not a symlink, owned by root
// or by the caller. A restore through a directory that another user
// controls could be redirected (plan question 4; a dirfd restore is M4).
func safeDir(src Change) error {
	return safeDirFor(src.Path, "restore", "; see it with: sc cat "+src.ID)
}

// safeDirFor is safeDir for any write of path: op is the verb of the
// messages ("restore", "edit"), hint ends a refusal.
func safeDirFor(path, op, hint string) error {
	dir := filepath.Dir(path)
	cur := "/"
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part != "" {
			cur = filepath.Join(cur, part)
		}
		fi, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("%s %s: %w (file not changed)", op, path, err)
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("refusing to %s into %s (a symlink)%s", op, cur, hint)
		case !fi.IsDir():
			return fmt.Errorf("%s %s: %s is not a directory (file not changed)", op, path, cur)
		}
		if uid, _ := ownerOf(fi); uid != 0 && uid != os.Geteuid() {
			return fmt.Errorf("refusing to %s into %s (owner uid %d, not root)%s", op, cur, uid, hint)
		}
		if cur == dir {
			break
		}
	}
	return nil
}

func ownerOf(fi os.FileInfo) (uid, gid int) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(st.Uid), int(st.Gid)
}

// savePreRestore reads what is at the path now (outside any lock) and
// records it as a pre-restore row, whatever it is. It returns the row (nil
// if the path is absent) and the stamp of the version it read.
func (s *Store) savePreRestore(src Change) (*Change, fsutil.Stamp, error) {
	st, err := fsutil.ReadState(src.Path)
	if fsutil.IsNotExist(err) {
		return nil, fsutil.Stamp{}, nil
	}
	if err != nil {
		return nil, fsutil.Stamp{}, err
	}
	if !st.Stable {
		return nil, fsutil.Stamp{}, errChanged
	}
	res, err := s.Record([]Obs{{Path: src.Path, State: &st, Force: true,
		Origin: OriginPreRestore, Intent: "before restoring " + src.ID}})
	if err != nil {
		return nil, fsutil.Stamp{}, err
	}
	pre := res[0].Change
	return &pre, st.Meta.Stamp, nil
}

// commitRestore records the restore and makes the change on disk, all
// under the write lock: anyone who sees the change and takes the write
// lock to record it (the watcher) waits for this commit and finds the
// restore row. It first checks that the path is still the version
// savePreRestore saved (existed and stamp), and returns errChanged if not.
func (s *Store) commitRestore(src Change, existed bool, stamp fsutil.Stamp, pending *fsutil.Pending) (restored Change, wrote bool, err error) {
	restored = Change{
		Path: src.Path, Kind: src.Kind, Blob: src.Blob, Target: src.Target, Size: src.Size,
		Mode: src.Mode, UID: src.UID, GID: src.GID,
		Origin: OriginRestore, Intent: "restored from " + src.ID,
	}
	wrote, err = s.commitWrite(&restored, existed, stamp, false, func() error {
		switch src.Kind {
		case KindLink:
			return fsutil.SymlinkAtomic(src.Path, src.Target, src.UID, src.GID)
		case KindDeleted:
			return fsutil.RemoveFile(src.Path)
		}
		return pending.Commit()
	})
	return restored, wrote, err
}

// commitWrite records row c and makes its change on disk with write, all
// under the write lock: anyone who sees the change and takes the write
// lock to record it (the watcher) waits for this commit and finds the row.
// It first checks that the path is still the version the caller read
// (existed and stamp), and returns errChanged if not. With absentFirst, a
// path that has no rows yet gets a "did not exist" row before c.
func (s *Store) commitWrite(c *Change, existed bool, stamp fsutil.Stamp, absentFirst bool, write func() error) (wrote bool, err error) {
	err = s.writeTx(true, func(ctx context.Context, conn *sql.Conn) error {
		st, err := fsutil.LstatStamp(c.Path)
		switch {
		case existed && (err != nil || st != stamp):
			return errChanged
		case !existed && !fsutil.IsNotExist(err):
			return errChanged
		}
		// Last point where stopping leaves the path untouched. Past it, a
		// forced stop (ForceStop) must wait for this write to report.
		pointMu.Lock()
		if stopping() || forced {
			pointMu.Unlock()
			return ErrInterrupted
		}
		passedPoint = true
		pointMu.Unlock()
		c.TS = s.now().Unix()
		if absentFirst {
			last, err := latestTx(ctx, conn, c.Path)
			if err != nil {
				return err
			}
			if last == nil {
				gone := Change{Path: c.Path, Kind: KindDeleted, Origin: c.Origin, Intent: "did not exist", TS: c.TS}
				if err := insertTx(ctx, conn, &gone); err != nil {
					return err
				}
			}
		}
		if err := insertTx(ctx, conn, c); err != nil {
			return err
		}
		if err := write(); err != nil {
			return err
		}
		wrote = true
		if testHookAfterRestoreWrite != nil {
			testHookAfterRestoreWrite()
		}
		return nil
	})
	return wrote, err
}

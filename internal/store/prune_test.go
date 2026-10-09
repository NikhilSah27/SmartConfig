package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"smartconfig/internal/fsutil"
)

const day = 24 * time.Hour

// ageBlobs sets every file's time in objects/ back two hours, past
// BlobGrace.
func ageBlobs(t *testing.T, s *Store) {
	t.Helper()
	old := time.Now().Add(-2 * time.Hour)
	filepath.WalkDir(filepath.Join(s.dir, "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chtimes(p, old, old)
		}
		return nil
	})
}

func autoRow(t *testing.T, s *Store, p, content string) Change {
	t.Helper()
	write(t, p, content, 0o644)
	return record(t, s, observe(t, p))[0].Change
}

func rowIDs(t *testing.T, s *Store, p string) (ids []string) {
	t.Helper()
	cs, err := s.List(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(cs) - 1; i >= 0; i-- { // oldest first
		ids = append(ids, cs[i].ID)
	}
	return ids
}

func wantRows(t *testing.T, s *Store, p string, want ...Change) {
	t.Helper()
	got := rowIDs(t, s, p)
	var ids []string
	for _, c := range want {
		ids = append(ids, c.ID)
	}
	if len(got) != len(ids) {
		t.Fatalf("%s keeps %v, want %v", filepath.Base(p), got, ids)
	}
	for i := range got {
		if got[i] != ids[i] {
			t.Fatalf("%s keeps %v, want %v", filepath.Base(p), got, ids)
		}
	}
}

// settable makes the store's clock settable: now is the time rows get.
func settable(s *Store) *time.Time {
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	return &now
}

// Prune deletes an automatic version only when a newer row replaced it
// before the cutoff; each path keeps its first and newest rows and its
// version at each boot line; manual rows stay (M5 follow-up 2, the chunk I
// review: I1, I2). A dry run says the same and changes nothing.
func TestPrune(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p, q := filepath.Join(dir, "p"), filepath.Join(dir, "q")
	p1 := autoRow(t, s, p, "one\n") // first row: kept
	*now = now.Add(day)
	p2 := autoRow(t, s, p, "two\n") // replaced at day 2: goes
	*now = now.Add(day)
	p3 := autoRow(t, s, p, "three\n") // the version at the line: kept
	q1 := autoRow(t, s, q, "q one\n")
	line, _ := s.NewestRowID()
	*now = now.Add(day)
	p4 := autoRow(t, s, p, "four\n") // replaced at day 4, by a manual row: goes
	*now = now.Add(day)
	write(t, p, "five\n", 0o644)
	p5, _, err := s.Snapshot(p, OriginManual, "by hand")
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(day)
	p6 := autoRow(t, s, p, "six\n") // replaced at day 6 = the cutoff: stays
	*now = now.Add(day)
	p7 := autoRow(t, s, p, "seven\n") // newest
	q2 := autoRow(t, s, q, "q two\n")
	cutoff := time.Unix(1_700_000_000, 0).Add(6 * day)
	lines := []int64{line, -1, line, 0} // -1 and 0: no row; duplicates are one line

	// Within BlobGrace no blob goes, though two are no longer used.
	if got, err := s.Prune(cutoff, lines, true); err != nil || got != (PruneResult{Rows: 2}) {
		t.Fatalf("dry run, new blobs: %+v %v", got, err)
	}
	ageBlobs(t, s)
	want := PruneResult{Rows: 2, Blobs: 2, Bytes: int64(len("two\nfour\n"))}
	if got, err := s.Prune(cutoff, lines, true); err != nil || got != want {
		t.Fatalf("dry run: %+v %v, want %+v", got, err, want)
	}
	if !s.HasObject(p2.Blob) || len(rowIDs(t, s, p)) != 7 {
		t.Fatal("the dry run changed something")
	}
	if got, err := s.Prune(cutoff, lines, false); err != nil || got != want {
		t.Fatalf("prune: %+v %v, want %+v", got, err, want)
	}
	wantRows(t, s, p, p1, p3, p5, p6, p7)
	wantRows(t, s, q, q1, q2)
	if s.HasObject(p2.Blob) || s.HasObject(p4.Blob) || !s.HasObject(p3.Blob) || !s.HasObject(p6.Blob) {
		t.Error("blobs: p2's and p4's gone, p3's and p6's kept")
	}
	if err := s.IntegrityCheck(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Prune(cutoff, lines, false); err != nil || got != (PruneResult{}) {
		t.Errorf("second prune: %+v %v", got, err)
	}
	// Without the line, p3 goes too; q1, q's first row, stays.
	if got, err := s.Prune(cutoff, nil, false); err != nil || got.Rows != 1 {
		t.Fatalf("no lines: %+v %v", got, err)
	}
	wantRows(t, s, p, p1, p5, p6, p7)
	wantRows(t, s, q, q1, q2)
	// A new row's rowid is still after every old one.
	before, _ := s.NewestRowID()
	*now = now.Add(day)
	autoRow(t, s, p, "eight\n")
	if after, _ := s.NewestRowID(); after <= before {
		t.Errorf("rowid %d after %d", after, before)
	}
}

// A version recorded after the cutoff stays even when the row after it
// says otherwise: the wall clock can step back (NTP, a paused VM).
func TestPruneClockSteppedBack(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	autoRow(t, s, p, "first\n")
	*now = now.Add(10 * day)
	x := autoRow(t, s, p, "recorded on day 10\n")
	*now = now.Add(-9 * day) // the clock steps back
	autoRow(t, s, p, "recorded on day 1\n")
	ageBlobs(t, s)
	if got, err := s.Prune(time.Unix(1_700_000_000, 0).Add(5*day), nil, false); err != nil || got.Rows != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.Get(x.ID); err != nil {
		t.Error(err)
	}
}

// The plan is read in pages: a page boundary inside a path, or between
// two, changes nothing.
func TestPrunePages(t *testing.T) {
	for _, page := range []int{1, 2, 3, 5000} {
		s, dir := setup(t)
		now := settable(s)
		old := prunePlanPage
		prunePlanPage = page
		var keep []Change
		for _, name := range []string{"a", "b", "c"} {
			p := filepath.Join(dir, name)
			keep = append(keep, autoRow(t, s, p, "1\n"))
			for i := 2; i < 6; i++ {
				*now = now.Add(time.Hour)
				autoRow(t, s, p, string(rune('0'+i))+"\n")
			}
			keep = append(keep, autoRow(t, s, p, "6\n"))
		}
		got, err := s.Prune(now.Add(day), nil, false)
		prunePlanPage = old
		if err != nil || got.Rows != 12 {
			t.Fatalf("page %d: %+v %v", page, got, err)
		}
		for i, name := range []string{"a", "b", "c"} {
			wantRows(t, s, filepath.Join(dir, name), keep[2*i], keep[2*i+1])
		}
	}
}

// I1: a version recorded 100 days ago that was the file's content until
// an hour ago stays: it is the undo of the newest change. A boot line from
// before it does not help (a server up for 100 days).
func TestPruneKeepsVersionLiveUntilRecently(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p, q := filepath.Join(dir, "p"), filepath.Join(dir, "q")
	autoRow(t, s, q, "other\n")
	line, _ := s.NewestRowID()
	*now = now.Add(time.Minute)
	autoRow(t, s, p, "first\n")
	*now = now.Add(time.Minute)
	x := autoRow(t, s, p, "the config in use for 100 days\n")
	*now = now.Add(100 * day)
	autoRow(t, s, p, "changed an hour ago\n")
	*now = now.Add(time.Hour)
	ageBlobs(t, s)
	if res, err := s.Prune(now.Add(-90*day), []int64{line}, false); err != nil || res.Rows != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := s.Get(x.ID); err != nil || !s.HasObject(x.Blob) {
		t.Errorf("the version p had until an hour ago, %s, is gone: %v", x.ID, err)
	}
}

// I1: sc edit's "before" row, the file as scd recorded it, stays while
// the edit is newer than the cutoff: its version was current until the
// edit. It is scd's own row (sc edit adds no row of the same state, M3);
// once the edit is older than the cutoff, it goes like any version
// replaced then.
func TestPruneKeepsStateBeforeEdit(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	autoRow(t, s, p, "first\n")
	*now = now.Add(time.Minute)
	x := autoRow(t, s, p, "as scd recorded it\n")
	*now = now.Add(100 * day)
	base, err := fsutil.ReadState(p)
	if err != nil {
		t.Fatal(err)
	}
	_, prev, err := s.Replace(p, []byte("edited\n"), 0o644, os.Geteuid(), os.Getegid(), &base, OriginEdit, "sc edit")
	if err != nil || prev == nil || prev.ID != x.ID {
		t.Fatalf("before: %+v %v (scd's row %s)", prev, err, x.ID)
	}
	*now = now.Add(time.Hour)
	ageBlobs(t, s)
	if got, err := s.Prune(now.Add(-90*day), nil, false); err != nil || got.Rows != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.Get(prev.ID); err != nil || !s.HasObject(prev.Blob) {
		t.Errorf("sc edit said \"before: %s\"; after sc prune: %v", prev.ID, err)
	}
	*now = now.Add(100 * day)
	if got, err := s.Prune(now.Add(-90*day), nil, false); err != nil || got.Rows != 1 {
		t.Errorf("100 days after the edit: %+v %v", got, err)
	}
}

// I3: sc snapshot of a file scd has recorded is a manual row, kept.
func TestPruneKeepsSnapshotDedupedOntoAuto(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	autoRow(t, s, p, "first\n")
	*now = now.Add(time.Minute)
	auto := autoRow(t, s, p, "known good\n")
	*now = now.Add(100 * day)
	c, unchanged, err := s.Snapshot(p, OriginManual, "before the upgrade")
	if err != nil || unchanged || c.ID == auto.ID || c.Origin != OriginManual {
		t.Fatalf("snapshot %+v unchanged %v: %v", c, unchanged, err)
	}
	// A second snapshot of the same file is unchanged since the first.
	if again, unchanged, err := s.Snapshot(p, OriginManual, "again"); err != nil || !unchanged || again.ID != c.ID {
		t.Fatalf("second snapshot %+v unchanged %v: %v", again, unchanged, err)
	}
	*now = now.Add(time.Minute)
	autoRow(t, s, p, "after the upgrade\n")
	*now = now.Add(100 * day)
	ageBlobs(t, s)
	if _, err := s.Prune(now.Add(-90*day), nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(c.ID); err != nil || !s.HasObject(c.Blob) {
		t.Errorf("sc snapshot gave %s; after sc prune: %v", c.ID, err)
	}
	if _, err := s.Get(auto.ID); err == nil {
		t.Error("scd's row of the same content was not pruned")
	}
}

// Blobs: only those no row uses, past BlobGrace; names that are not a
// blob's stay; a killed writer's temp files go; one that cannot be removed
// is skipped and counted, and the rest still go.
func TestPruneBlobs(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	autoRow(t, s, p, "first\n")
	a := autoRow(t, s, p, "A\n")
	*now = now.Add(day)
	b := autoRow(t, s, p, "B\n")
	*now = now.Add(day)
	autoRow(t, s, p, "C\n")
	objects := filepath.Join(s.dir, "objects")
	pre := a.Blob[:2]
	junk := []string{
		filepath.Join(objects, pre, pre+"not-a-blob"),
		filepath.Join(objects, pre, a.Blob[:63]+"g"), // 64 characters, not hex
		filepath.Join(objects, "zz", a.Blob),         // a blob's name in the wrong directory
	}
	for _, j := range junk {
		os.MkdirAll(filepath.Dir(j), 0o700)
		os.WriteFile(j, []byte("x"), 0o600)
	}
	tmp := filepath.Join(objects, pre, "."+a.Blob+".sc-tmp-123")
	os.WriteFile(tmp, []byte("half"), 0o600)
	ageBlobs(t, s)
	want := PruneResult{Rows: 2, Blobs: 3, Bytes: int64(len("A\nB\nhalf"))}
	if got, err := s.Prune(now.Add(time.Hour), nil, false); err != nil || got != want {
		t.Fatalf("%+v %v, want %+v", got, err, want)
	}
	for _, j := range junk {
		if _, err := os.Stat(j); err != nil {
			t.Errorf("not a blob, removed: %s", j)
		}
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Error("the temp file is still there")
	}
	if os.Geteuid() == 0 {
		return // root removes files whatever the directory's mode
	}
	// One blob that cannot be removed does not stop the others.
	*now = now.Add(day)
	d := autoRow(t, s, p, "D\n")
	*now = now.Add(day)
	e := autoRow(t, s, p, "E\n")
	*now = now.Add(day)
	autoRow(t, s, p, "F\n")
	ageBlobs(t, s)
	stuck := filepath.Dir(s.blobPath(d.Blob))
	if stuck == filepath.Dir(s.blobPath(e.Blob)) {
		t.Skip("both blobs in one directory")
	}
	os.Chmod(stuck, 0o500)
	defer os.Chmod(stuck, 0o700)
	got, err := s.Prune(now.Add(time.Hour), nil, false)
	if err != nil || got.Rows != 3 || got.Skipped != 1 || got.SkipErr == nil || s.HasObject(e.Blob) || !s.HasObject(d.Blob) {
		t.Errorf("%+v %v", got, err)
	}
	_ = b
}

// A writer that found its blob before taking the lock puts it again under
// the lock if sc prune deleted it meanwhile.
func TestPruneRecordRace(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	autoRow(t, s, p, "first\n")
	a := autoRow(t, s, p, "A\n")
	*now = now.Add(day)
	autoRow(t, s, p, "B\n")
	ageBlobs(t, s)
	write(t, p, "A\n", 0o644) // back to A: its blob exists, so Record does not put it
	pruned := false
	testHookSnapshotBeforeLock = func() {
		if !pruned {
			pruned = true
			if got, err := s.Prune(now.Add(time.Hour), nil, false); err != nil || got.Blobs != 1 {
				t.Errorf("prune in the gap: %+v %v", got, err)
			}
		}
	}
	t.Cleanup(func() { testHookSnapshotBeforeLock = nil })
	*now = now.Add(day)
	c := record(t, s, observe(t, p))[0].Change
	if !pruned || c.Blob != a.Blob {
		t.Fatalf("pruned %v, row %+v", pruned, c)
	}
	if _, err := s.Blob(c.Blob); err != nil {
		t.Errorf("the new row's blob: %v", err)
	}
}

// The same for a restore (commitWrite): the version it restores may lose
// its row and blob to sc prune after it read them.
func TestPruneRestoreRace(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	autoRow(t, s, p, "first\n")
	a := autoRow(t, s, p, "A\n")
	*now = now.Add(day)
	autoRow(t, s, p, "B\n")
	ageBlobs(t, s)
	testHookBeforePreRestore = func() {
		if got, err := s.Prune(now.Add(time.Hour), nil, false); err != nil || got.Rows != 1 || got.Blobs != 1 {
			t.Errorf("prune in the gap: %+v %v", got, err)
		}
	}
	t.Cleanup(func() { testHookBeforePreRestore = nil })
	*now = now.Add(day)
	r, _, err := s.Restore(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := s.Blob(r.Blob); err != nil || string(b) != "A\n" {
		t.Errorf("the restore row's blob: %q %v", b, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "A\n" {
		t.Errorf("file: %q", got)
	}
}

// The same for sc edit's write (Replace, commitWrite with its data).
func TestPruneReplaceRace(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p, q := filepath.Join(dir, "p"), filepath.Join(dir, "q")
	autoRow(t, s, q, "first\n")
	a := autoRow(t, s, q, "A\n")
	*now = now.Add(day)
	autoRow(t, s, q, "B\n")
	autoRow(t, s, p, "p\n")
	ageBlobs(t, s)
	testHookBeforeReplaceLock = func() {
		if got, err := s.Prune(now.Add(time.Hour), nil, false); err != nil || got.Blobs != 1 {
			t.Errorf("prune in the gap: %+v %v", got, err)
		}
	}
	t.Cleanup(func() { testHookBeforeReplaceLock = nil })
	base, err := fsutil.ReadState(p)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(day)
	row, _, err := s.Replace(p, []byte("A\n"), 0o644, os.Geteuid(), os.Getegid(), &base, OriginEdit, "sc edit")
	if err != nil || row.Blob != a.Blob {
		t.Fatalf("%+v %v", row, err)
	}
	if b, err := s.Blob(row.Blob); err != nil || string(b) != "A\n" {
		t.Errorf("sc edit's row's blob: %q %v", b, err)
	}
}

// A row recorded after the plan, while the blobs are being deleted, keeps
// the blob it uses, though the plan saw no row using it.
func TestPruneNewRowKeepsItsBlob(t *testing.T) {
	s, dir := setup(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	autoRow(t, s, p, "first\n")
	a := autoRow(t, s, p, "A\n")
	*now = now.Add(day)
	autoRow(t, s, p, "B\n")
	ageBlobs(t, s)
	var c Change
	testHookPruneBlobs = func() {
		// scd records p back at A, and commits, before the blob batch.
		c = autoRow(t, s, p, "A\n")
	}
	t.Cleanup(func() { testHookPruneBlobs = nil })
	*now = now.Add(day)
	got, err := s.Prune(now.Add(-time.Hour), nil, false)
	if err != nil || got.Rows != 1 || got.Blobs != 0 || c.Blob != a.Blob {
		t.Fatalf("%+v %v %+v", got, err, c)
	}
	if _, err := s.Blob(c.Blob); err != nil {
		t.Errorf("the new row's blob: %v", err)
	}
}

// After Interrupt, Prune stops between batches with what it did: before
// the first row batch nothing is deleted; between the rows and the blobs
// the rows are gone and no blob is touched.
func TestPruneInterrupt(t *testing.T) {
	s, dir := setup(t)
	resetInterrupt(t)
	now := settable(s)
	p := filepath.Join(dir, "p")
	for i := 0; i < 6; i++ {
		autoRow(t, s, p, string(rune('a'+i))+"\n")
		*now = now.Add(time.Hour)
	}
	ageBlobs(t, s)
	Interrupt()
	if got, err := s.Prune(now.Add(day), nil, false); !errors.Is(err, ErrInterrupted) || got.Rows != 0 || len(rowIDs(t, s, p)) != 6 {
		t.Fatalf("interrupted first: %+v %v", got, err)
	}
	interrupted.Store(false)
	testHookPruneBlobs = Interrupt
	t.Cleanup(func() { testHookPruneBlobs = nil })
	got, err := s.Prune(now.Add(day), nil, false)
	if !errors.Is(err, ErrInterrupted) || got.Rows != 4 || got.Blobs != 0 || len(rowIDs(t, s, p)) != 2 {
		t.Fatalf("interrupted before the blobs: %+v %v", got, err)
	}
	// The next run deletes the blobs.
	interrupted.Store(false)
	testHookPruneBlobs = nil
	if got, err := s.Prune(now.Add(day), nil, false); err != nil || got.Rows != 0 || got.Blobs != 4 {
		t.Errorf("the next run: %+v %v", got, err)
	}
}

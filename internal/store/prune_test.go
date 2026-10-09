package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ageBlobs sets every object's file time back two hours, past BlobGrace.
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
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return ids
}

// Prune deletes old automatic rows but each path's newest, its version at
// each boot line and every manual row (M5 follow-up 2); then the blobs no
// row uses, past BlobGrace; a dry run says the same and changes nothing.
func TestPrune(t *testing.T) {
	s, dir := setup(t)
	tick := clock(s)
	p, q := filepath.Join(dir, "p"), filepath.Join(dir, "q")
	r1 := autoRow(t, s, p, "one\n")
	tick()
	q1 := autoRow(t, s, q, "q one\n")
	tick()
	r2 := autoRow(t, s, p, "two\n")
	line, err := s.NewestRowID() // a healthy boot's verdict: p is r2, q is q1
	if err != nil {
		t.Fatal(err)
	}
	tick()
	write(t, p, "three\n", 0o644)
	r3, _, err := s.Snapshot(p, OriginManual, "by hand")
	if err != nil {
		t.Fatal(err)
	}
	tick()
	r4 := autoRow(t, s, p, "four\n")
	tick()
	q2 := autoRow(t, s, q, "q two\n")
	// Names in objects/ that are not blobs are left alone.
	junk := []string{filepath.Join(s.dir, "objects", r1.Blob[:2], ".tmp-123"),
		filepath.Join(s.dir, "objects", "zz", r1.Blob)}
	for _, j := range junk {
		os.MkdirAll(filepath.Dir(j), 0o700)
		os.WriteFile(j, []byte("x"), 0o600)
	}
	cutoff := time.Unix(1_700_000_000, 0).Add(time.Hour)
	lines := []int64{line, -1, line} // -1: a verdict without a row; duplicates are one line

	// Within BlobGrace no blob goes, though r1's is no longer used.
	if got, err := s.Prune(cutoff, lines, true); err != nil || got != (PruneResult{Rows: 1}) {
		t.Fatalf("dry run, new blobs: %+v %v", got, err)
	}
	ageBlobs(t, s)
	want := PruneResult{Rows: 1, Blobs: 1, Bytes: int64(len("one\n"))}
	if got, err := s.Prune(cutoff, lines, true); err != nil || got != want {
		t.Fatalf("dry run: %+v %v, want %+v", got, err, want)
	}
	if !s.HasObject(r1.Blob) || len(rowIDs(t, s, p)) != 4 {
		t.Fatal("the dry run changed something")
	}
	if got, err := s.Prune(cutoff, lines, false); err != nil || got != want {
		t.Fatalf("prune: %+v %v, want %+v", got, err, want)
	}
	if got := rowIDs(t, s, p); len(got) != 3 || got[0] != r4.ID || got[1] != r3.ID || got[2] != r2.ID {
		t.Errorf("p keeps %v, want r4 r3 r2 (%s %s %s)", got, r4.ID, r3.ID, r2.ID)
	}
	if got := rowIDs(t, s, q); len(got) != 2 || got[0] != q2.ID || got[1] != q1.ID {
		t.Errorf("q keeps %v", got)
	}
	if s.HasObject(r1.Blob) || !s.HasObject(r2.Blob) || !s.HasObject(q1.Blob) {
		t.Error("blobs: r1's should be gone, r2's and q1's kept")
	}
	for _, j := range junk {
		if _, err := os.Stat(j); err != nil {
			t.Errorf("not a blob, removed: %s", j)
		}
	}
	if err := s.IntegrityCheck(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Prune(cutoff, lines, false); err != nil || got != (PruneResult{}) {
		t.Errorf("second prune: %+v %v", got, err)
	}

	// Without the boot line, r2 and q1 go too; a row newer than the
	// cutoff stays.
	if got, err := s.Prune(cutoff, nil, false); err != nil || got.Rows != 2 {
		t.Fatalf("no lines: %+v %v", got, err)
	}
	if got := rowIDs(t, s, p); len(got) != 2 || got[0] != r4.ID || got[1] != r3.ID {
		t.Errorf("p keeps %v, want r4 r3", got)
	}
	tick()
	autoRow(t, s, q, "q three\n")
	if got, err := s.Prune(time.Unix(1_700_000_000, 0), nil, false); err != nil || got.Rows != 0 {
		t.Errorf("cutoff before every row: %+v %v", got, err)
	}
	// A new row's rowid is still after every old one.
	before, _ := s.NewestRowID()
	tick()
	autoRow(t, s, p, "five\n")
	if after, _ := s.NewestRowID(); after <= before {
		t.Errorf("rowid %d after %d", after, before)
	}
}

// A writer that found its blob before taking the lock puts it again under
// the lock if sc prune deleted it meanwhile (M5 follow-up 2).
func TestPruneRecordRace(t *testing.T) {
	s, dir := setup(t)
	tick := clock(s)
	p := filepath.Join(dir, "p")
	a := autoRow(t, s, p, "A\n")
	tick()
	autoRow(t, s, p, "B\n")
	ageBlobs(t, s)
	write(t, p, "A\n", 0o644) // back to A: its blob exists, so Record does not put it
	pruned := false
	testHookSnapshotBeforeLock = func() {
		if !pruned {
			pruned = true
			if got, err := s.Prune(time.Unix(1_700_000_000, 0).Add(time.Hour), nil, false); err != nil || got.Blobs != 1 {
				t.Errorf("prune in the gap: %+v %v", got, err)
			}
		}
	}
	t.Cleanup(func() { testHookSnapshotBeforeLock = nil })
	tick()
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
	tick := clock(s)
	p := filepath.Join(dir, "p")
	a := autoRow(t, s, p, "A\n")
	tick()
	autoRow(t, s, p, "B\n")
	ageBlobs(t, s)
	testHookBeforePreRestore = func() {
		if got, err := s.Prune(time.Unix(1_700_000_000, 0).Add(time.Hour), nil, false); err != nil || got.Rows != 1 || got.Blobs != 1 {
			t.Errorf("prune in the gap: %+v %v", got, err)
		}
	}
	t.Cleanup(func() { testHookBeforePreRestore = nil })
	tick()
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

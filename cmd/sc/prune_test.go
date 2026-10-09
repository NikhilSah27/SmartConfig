package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/boot"
	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// pruneEnv is a store with two automatic rows of p (the older one, and its
// blob, prunable once 100 days have passed) and one manual row of q. It
// returns p and the older row's id.
func pruneEnv(t *testing.T) (home, p, oldID string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	mustSC(t, "init")
	dir := t.TempDir()
	p, q := filepath.Join(dir, "p"), filepath.Join(dir, "q")
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, content := range []string{"old\n", "new\n"} {
		os.WriteFile(p, []byte(content), 0o644)
		st, err := fsutil.ReadState(p)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Record([]store.Obs{{Path: p, State: &st, Origin: store.OriginAuto}})
		if err != nil {
			t.Fatal(err)
		}
		if oldID == "" {
			oldID = res[0].Change.ID
		}
	}
	os.WriteFile(q, []byte("by hand\n"), 0o644)
	mustSC(t, "snapshot", q)
	old := time.Now().Add(-2 * time.Hour) // past store.BlobGrace
	filepath.WalkDir(filepath.Join(home, "objects"), func(f string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chtimes(f, old, old)
		}
		return nil
	})
	return home, p, oldID
}

func pruneAt(t *testing.T, at time.Time, answers string) {
	t.Helper()
	oldNow, oldIn := pruneNow, pruneStdin
	pruneNow, pruneStdin = func() time.Time { return at }, strings.NewReader(answers)
	t.Cleanup(func() { pruneNow, pruneStdin = oldNow, oldIn })
}

// sc prune says what can go and asks; anything but y, and end of input,
// deletes nothing; y and --yes delete (M5 follow-up 2).
func TestPrune(t *testing.T) {
	_, p, oldID := pruneEnv(t)
	later := time.Now().Add(100 * 24 * time.Hour)

	pruneAt(t, time.Now(), "")
	if r := sc(t, "prune"); r.code != 0 || !strings.HasPrefix(r.stdout, "nothing to prune: no automatic row from before ") {
		t.Fatalf("young rows: %+v", r)
	}
	for _, answer := range []string{"", "n\n", "yes please\n"} {
		pruneAt(t, later, answer)
		r := sc(t, "prune")
		if r.code != 0 || !strings.Contains(r.stdout, "1 row recorded by scd before ") ||
			!strings.Contains(r.stdout, "\nand 1 stored version (0.0 MiB) that no row will use any more.\n") ||
			!strings.Contains(r.stdout, "Delete them? [y/N]: ") || !strings.HasSuffix(r.stdout, "nothing deleted\n") ||
			(answer == "") != strings.HasSuffix(r.stdout, "[y/N]: \nnothing deleted\n") {
			t.Fatalf("answer %q: %+v", answer, r)
		}
		if !strings.Contains(mustSC(t, "log", p), oldID) {
			t.Fatalf("answer %q deleted the row", answer)
		}
	}
	pruneAt(t, later, "Y\n")
	if r := sc(t, "prune"); r.code != 0 || !strings.HasSuffix(r.stdout, "deleted 1 row and 1 stored version (0.0 MiB)\n") {
		t.Fatalf("y: %+v", r)
	}
	if strings.Contains(mustSC(t, "log", p), oldID) {
		t.Error("the old row is still there")
	}
	if r := sc(t, "cat", oldID); r.code != 1 {
		t.Errorf("cat of a pruned row: %+v", r)
	}
	if r := sc(t, "prune", "--yes"); r.code != 0 || !strings.HasPrefix(r.stdout, "nothing to prune") {
		t.Errorf("again: %+v", r)
	}
}

// --yes does not ask; --older-than counts from now.
func TestPruneYesOlderThan(t *testing.T) {
	pruneEnv(t)
	pruneAt(t, time.Now().Add(37*time.Hour), "")
	if r := sc(t, "prune", "--older-than", "2d", "--yes"); r.code != 0 || !strings.HasPrefix(r.stdout, "nothing to prune") {
		t.Errorf("2d after 37 h: %+v", r)
	}
	if r := sc(t, "prune", "--older-than", "36h", "-y"); r.code != 0 || strings.Contains(r.stdout, "Delete them?") ||
		!strings.HasSuffix(r.stdout, "deleted 1 row and 1 stored version (0.0 MiB)\n") {
		t.Errorf("36h after 37 h: %+v", r)
	}
}

// A row that is a file's version at a recorded boot stays: sc status
// compares with it. The older row goes, and the newest stays.
func TestPruneKeepsBootVersion(t *testing.T) {
	home, p, oldID := pruneEnv(t)
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := s.NewestRowID() // a verdict now: p's version is "new"
	atBoot, err := s.AsOf(p, line)
	if err != nil || atBoot == nil {
		t.Fatal(atBoot, err)
	}
	os.WriteFile(p, []byte("newer\n"), 0o644)
	st, _ := fsutil.ReadState(p)
	if _, err := s.Record([]store.Obs{{Path: p, State: &st, Origin: store.OriginAuto}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), line, "local-fs=active")
	pruneAt(t, time.Now().Add(100*24*time.Hour), "")
	if r := sc(t, "prune", "--yes"); r.code != 0 || !strings.HasSuffix(r.stdout, "deleted 1 row and 1 stored version (0.0 MiB)\n") {
		t.Fatalf("%+v", r)
	}
	if log := mustSC(t, "log", p); strings.Contains(log, oldID) || !strings.Contains(log, atBoot.ID) {
		t.Errorf("want %s gone and %s kept:\n%s", oldID, atBoot.ID, log)
	}
}

// A boots file sc cannot read: nothing is pruned. A bad --older-than is
// refused before anything is read.
func TestPruneRefuses(t *testing.T) {
	home, p, oldID := pruneEnv(t)
	pruneAt(t, time.Now().Add(100*24*time.Hour), "y\n")
	for _, bad := range []string{"0d", "-5d", "+5d", "90", "d", "5x", "1.5d", "36501d", "99999999999999h"} {
		if r := sc(t, "prune", "--older-than", bad); r.code != 1 || !strings.Contains(r.stderr, "--older-than") {
			t.Errorf("--older-than %s: %+v", bad, r)
		}
	}
	os.Mkdir(filepath.Join(home, boot.FileName), 0o700)
	if r := sc(t, "prune", "--yes"); r.code != 1 || !strings.Contains(r.stderr, "nothing pruned") {
		t.Errorf("unreadable boots file: %+v", r)
	}
	if !strings.Contains(mustSC(t, "log", p), oldID) {
		t.Error("pruned without the boots file")
	}
}

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

// autoRec records p as scd does (origin auto): data, or absent when data
// is "". It returns the row's id.
func autoRec(t *testing.T, p, data string) string {
	t.Helper()
	o := store.Obs{Path: p, Origin: store.OriginAuto}
	if data != "" {
		os.WriteFile(p, []byte(data), 0o644)
		st, err := fsutil.ReadState(p)
		if err != nil {
			t.Fatal(err)
		}
		o.State = &st
	} else {
		os.Remove(p)
	}
	s, err := store.Open(os.Getenv("SC_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Record([]store.Obs{o})
	if err != nil || !res[0].Recorded {
		t.Fatalf("record %s: %v %+v", p, err, res)
	}
	return res[0].Change.ID
}

// ageObjects sets every file's time in the store's objects/ back two
// hours, past store.BlobGrace.
func ageObjects(home string) {
	old := time.Now().Add(-2 * time.Hour)
	filepath.WalkDir(filepath.Join(home, "objects"), func(f string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chtimes(f, old, old)
		}
		return nil
	})
}

// pruneEnv is a store with a healthy boot and three automatic rows of p:
// its first (kept), the old one (prunable once 100 days have passed, with
// its blob) and the newest; and one manual row of q. No scd runs. It
// returns p and the old row's id.
func pruneEnv(t *testing.T) (home, p, oldID string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	mustSC(t, "init")
	old := procDir
	procDir = t.TempDir()
	t.Cleanup(func() { procDir = old })
	dir := t.TempDir()
	p, q := filepath.Join(dir, "p"), filepath.Join(dir, "q")
	autoRec(t, p, "first\n")
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := s.NewestRowID()
	s.Close()
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), line, "local-fs=active")
	oldID = autoRec(t, p, "old\n")
	autoRec(t, p, "new\n")
	os.WriteFile(q, []byte("by hand\n"), 0o644)
	mustSC(t, "snapshot", q)
	ageObjects(home)
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
	if r := sc(t, "prune"); r.code != 0 || !strings.HasPrefix(r.stdout, "nothing to prune: no version scd recorded was replaced before ") ||
		!strings.HasSuffix(r.stdout, ",\nand no stored content is unused\n") {
		t.Fatalf("young rows: %+v", r)
	}
	for _, answer := range []string{"", "n\n", "yes please\n"} {
		pruneAt(t, later, answer)
		r := sc(t, "prune")
		if r.code != 0 || !strings.HasPrefix(r.stdout, "1 version recorded by scd and replaced before ") ||
			!strings.Contains(r.stdout, "\nand 4 bytes of stored content no row will use any more.\n") ||
			!strings.Contains(r.stdout, "Delete them? [y/N]: ") || !strings.HasSuffix(r.stdout, "nothing deleted\n") ||
			(answer == "") != strings.HasSuffix(r.stdout, "[y/N]: \nnothing deleted\n") {
			t.Fatalf("answer %q: %+v", answer, r)
		}
		if !strings.Contains(mustSC(t, "log", p), oldID) {
			t.Fatalf("answer %q deleted the row", answer)
		}
	}
	pruneAt(t, later, "Y\n")
	if r := sc(t, "prune"); r.code != 0 || !strings.HasSuffix(r.stdout, "deleted 1 version and 4 bytes of stored content\n") {
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
		!strings.HasSuffix(r.stdout, "deleted 1 version and 4 bytes of stored content\n") {
		t.Errorf("36h after 37 h: %+v", r)
	}
}

// A row that is a file's version at a recorded boot stays, though a newer
// row replaced it long ago: sc status compares with it.
func TestPruneKeepsBootVersion(t *testing.T) {
	home, p, oldID := pruneEnv(t)
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := s.NewestRowID() // a verdict now: p's version is "new"
	atBoot, err := s.AsOf(p, line)
	s.Close()
	if err != nil || atBoot == nil {
		t.Fatal(atBoot, err)
	}
	autoRec(t, p, "newer\n")
	boot.Record(home, "bbbbbbbb-2", "ok", time.Now(), line, "local-fs=active")
	pruneAt(t, time.Now().Add(100*24*time.Hour), "")
	if r := sc(t, "prune", "--yes"); r.code != 0 || !strings.HasSuffix(r.stdout, "deleted 1 version and 4 bytes of stored content\n") {
		t.Fatalf("%+v", r)
	}
	if log := mustSC(t, "log", p); strings.Contains(log, oldID) || !strings.Contains(log, atBoot.ID) {
		t.Errorf("want %s gone and %s kept:\n%s", oldID, atBoot.ID, log)
	}
}

// Stored content no row uses goes though no row does; what could not be
// removed is said, after what was deleted.
func TestPruneOnlyBlobs(t *testing.T) {
	home, _, _ := pruneEnv(t)
	orphan := filepath.Join(home, "objects", "ab", "ab"+strings.Repeat("0", 62))
	os.MkdirAll(filepath.Dir(orphan), 0o700)
	os.WriteFile(orphan, []byte(strings.Repeat("x", 3000)), 0o600)
	ageObjects(home)
	pruneAt(t, time.Now(), "y\n")
	r := sc(t, "prune")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "0 versions recorded by scd and replaced before ") ||
		!strings.Contains(r.stdout, "\nand 3 KiB of stored content no row will use any more.\n") ||
		!strings.HasSuffix(r.stdout, "deleted 0 versions and 3 KiB of stored content\n") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("still there")
	}
	if os.Geteuid() == 0 {
		return // root removes files whatever the directory's mode
	}
	os.WriteFile(orphan, []byte("x"), 0o600)
	ageObjects(home)
	os.Chmod(filepath.Dir(orphan), 0o500)
	defer os.Chmod(filepath.Dir(orphan), 0o700)
	r = sc(t, "prune", "--yes")
	if r.code != 0 || !strings.HasSuffix(r.stdout, "deleted 0 versions and 0 bytes of stored content\n1 stored file could not be removed (remove "+orphan+": permission denied)\n") {
		t.Errorf("%+v", r)
	}
}

// Nothing is pruned: a bad --older-than (refused before anything is read),
// a boots file sc cannot read, no healthy boot yet, an scd an upgrade
// replaced (the chunk I review, I2 and I4).
func TestPruneRefuses(t *testing.T) {
	home, p, oldID := pruneEnv(t)
	pruneAt(t, time.Now().Add(100*24*time.Hour), "y\n")
	for _, bad := range []string{"0d", "-5d", "+5d", "90", "d", "5x", "1.5d", "36501d", "99999999999999h"} {
		if r := sc(t, "prune", "--older-than", bad); r.code != 1 || !strings.Contains(r.stderr, "--older-than") {
			t.Errorf("--older-than %s: %+v", bad, r)
		}
	}
	// scd runs a program an upgrade replaced on disk; then one it did not.
	pid := filepath.Join(procDir, "4242")
	os.Mkdir(pid, 0o700)
	os.WriteFile(filepath.Join(pid, "cmdline"), []byte("/usr/sbin/sc\x00watch\x00"), 0o600)
	os.Symlink("/usr/sbin/sc (deleted)", filepath.Join(pid, "exe"))
	if r := sc(t, "prune", "--yes"); r.code != 1 || !strings.Contains(r.stderr, "scd (pid 4242) still runs the sc an upgrade replaced: run sudo systemctl restart scd") {
		t.Errorf("scd replaced: %+v", r)
	}
	os.Remove(filepath.Join(pid, "exe"))
	os.Symlink("/usr/sbin/sc", filepath.Join(pid, "exe"))
	bootsFile := filepath.Join(home, boot.FileName)
	os.Rename(bootsFile, bootsFile+".kept")
	if r := sc(t, "prune", "--yes"); r.code != 1 || !strings.Contains(r.stderr, "no healthy boot is recorded yet") {
		t.Errorf("no boots file: %+v", r)
	}
	boot.Record(home, "aaaaaaaa-1", "bad", time.Now(), 1, "local-fs=inactive")
	boot.Record(home, "bbbbbbbb-2", "ok", time.Now(), -1, "local-fs=active")
	if r := sc(t, "prune", "--yes"); r.code != 1 || !strings.Contains(r.stderr, "no healthy boot is recorded yet") {
		t.Errorf("no ok verdict with a row: %+v", r)
	}
	os.Remove(bootsFile)
	os.Mkdir(bootsFile, 0o700)
	if r := sc(t, "prune", "--yes"); r.code != 1 || !strings.Contains(r.stderr, "nothing pruned") {
		t.Errorf("unreadable boots file: %+v", r)
	}
	if !strings.Contains(mustSC(t, "log", p), oldID) {
		t.Error("pruned when it should not")
	}
	os.Remove(bootsFile)
	os.Rename(bootsFile+".kept", bootsFile)
	if r := sc(t, "prune", "--yes"); r.code != 0 || !strings.HasPrefix(r.stdout, "1 version") {
		t.Errorf("all well again: %+v", r)
	}
}

// statusAfterPrune runs sc status, then sc prune --yes 100 days later,
// then sc status again, and returns both reports.
func statusAfterPrune(t *testing.T) (before, after string) {
	t.Helper()
	before = sc(t, "status").stdout
	ageObjects(os.Getenv("SC_HOME"))
	pruneAt(t, time.Now().Add(100*24*time.Hour), "")
	r := sc(t, "prune", "--yes")
	if r.code != 0 && !strings.Contains(r.stderr, "nothing pruned") {
		t.Fatalf("prune: %+v", r)
	}
	return before, sc(t, "status").stdout
}

// The chunk I review, I2 (a): with no healthy boot, sc status compares
// the newest change with the row before it; sc prune deletes nothing then,
// so the undo stays.
func TestPruneStatusUndoNoHealthyBoot(t *testing.T) {
	_, fstab, _ := statusEnv(t, "BOOT_IMAGE=/vmlinuz root=UUID=x ro", false)
	good := autoRec(t, fstab, goodLine)
	autoRec(t, fstab, badLine)
	autoRec(t, fstab, badLine+"# again\n")
	before, after := statusAfterPrune(t)
	if !strings.Contains(before, "  sc restore "+good+"\n") || after != before {
		t.Errorf("the undo changed.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// I2 (b): a file created after the last healthy boot: its "did not exist"
// row, its first, is the proof that it is new, and stays.
func TestPruneStatusUndoNewFile(t *testing.T) {
	dir, fstab, home := statusEnv(t, "BOOT_IMAGE=/vmlinuz root=UUID=x ro", false)
	autoRec(t, filepath.Join(dir, "motd"), "hello\n")
	healthy := time.Now().Add(-time.Hour)
	boot.Seen(home, "aaaaaaaa-1", healthy)
	boot.Record(home, "aaaaaaaa-1", "ok", healthy, newestRow(t), "local-fs=active")
	created(t, fstab, goodLine)
	autoRec(t, fstab, badLine)
	boot.Seen(home, "bbbbbbbb-2", time.Now().Add(-time.Minute))
	before, after := statusAfterPrune(t)
	want := "\nTo undo " + fstab + ", which is new, move it aside:\n"
	if !strings.Contains(before, want) || !strings.Contains(after, want) {
		t.Errorf("the undo of a new file changed.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// I2 (c): a file first seen after the last healthy boot, deleted, a boot
// that failed, then the file again: its first row stays, and sc status
// does not call it new (the M4 final review's B2).
func TestPruneStatusUndoFileNotNew(t *testing.T) {
	dir, fstab, home := statusEnv(t, "BOOT_IMAGE=/vmlinuz root=UUID=x ro", false)
	autoRec(t, filepath.Join(dir, "motd"), "hello\n")
	healthy := time.Now().Add(-2 * time.Hour)
	boot.Seen(home, "aaaaaaaa-1", healthy)
	boot.Record(home, "aaaaaaaa-1", "ok", healthy, newestRow(t), "local-fs=active")
	first := autoRec(t, fstab, goodLine)
	autoRec(t, fstab, "")
	bad := time.Now().Add(-time.Hour)
	boot.Seen(home, "bbbbbbbb-2", bad)
	boot.Record(home, "bbbbbbbb-2", "bad", bad, newestRow(t), "local-fs=inactive")
	autoRec(t, fstab, badLine)
	before, after := statusAfterPrune(t)
	want := "  sc restore " + first + "\n"
	if !strings.Contains(before, want) || !strings.Contains(after, want) || strings.Contains(after, "move it aside") {
		t.Errorf("the file is called new.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

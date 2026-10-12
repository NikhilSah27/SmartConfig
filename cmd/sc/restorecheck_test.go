package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/check"
	"smartconfig/internal/store"
)

func restoreAnswers(t *testing.T, answers string) {
	t.Helper()
	old := restoreStdin
	restoreStdin = strings.NewReader(answers)
	t.Cleanup(func() { restoreStdin = old })
	answeredAtTerminal(t)
}

// answeredAtTerminal makes the commands ask, as at a terminal, while the
// test feeds the answers.
func answeredAtTerminal(t *testing.T) {
	t.Helper()
	old := atTerminal
	atTerminal = func(io.Reader, io.Writer) bool { return true }
	t.Cleanup(func() { atTerminal = old })
}

// sc restore checks the version it is about to write against the file as
// it is now (M5 follow-up 3). A version that adds a blocker or an error
// is listed and asked about: end of input, or anything but y, restores
// nothing (exit 2); y and --force restore it. One that adds nothing, as
// the healthy version a rescue report gives, restores as before, with no
// word more.
func TestRestoreChecks(t *testing.T) {
	dir, fstab := checkEnv(t)
	mustSC(t, "init")
	good := snap(t, fstab, goodLine)
	bad := snap(t, fstab, badLine)
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	rows := func() int { return strings.Count(mustSC(t, "log", fstab), "\n") }
	n := rows()
	for _, answer := range []string{"", "n\n", "restore\n"} {
		restoreAnswers(t, answer)
		r := sc(t, "restore", bad)
		if r.code != 2 || !strings.Contains(r.stdout, "blocker") || !strings.Contains(r.stdout, "fstab-source-missing") ||
			!strings.Contains(r.stdout, "\nRestoring "+bad+" adds 1 blocker compared with "+fstab+" as it is now.\n") ||
			!strings.Contains(r.stdout, "Restore anyway? [y/N]: ") ||
			!strings.HasSuffix(r.stdout, "not restored: "+fstab+" is unchanged\n") {
			t.Fatalf("answer %q: %+v", answer, r)
		}
		if b, _ := os.ReadFile(fstab); string(b) != goodLine || rows() != n {
			t.Fatalf("answer %q changed something: %q, %d rows", answer, b, rows())
		}
	}
	restoreAnswers(t, "Y\n")
	if r := sc(t, "restore", bad); r.code != 0 || !strings.Contains(r.stdout, "[y/N]: restored "+fstab+" from "+bad+" (") {
		t.Fatalf("y: %+v", r)
	}
	if b, _ := os.ReadFile(fstab); string(b) != badLine {
		t.Fatalf("y did not restore: %q", b)
	}
	// The healthy version over the broken one: nothing added, no question,
	// the one line it always printed.
	restoreAnswers(t, "")
	r := sc(t, "restore", good)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "restored "+fstab+" from "+good+" (") || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("the healthy version: %+v", r)
	}
	// --force does not ask.
	if r := sc(t, "restore", "--force", bad); r.code != 0 || strings.Contains(r.stdout, "Restore anyway?") ||
		!strings.HasPrefix(r.stdout, "restored ") {
		t.Fatalf("--force: %+v", r)
	}
	// A file no checker reads is restored as before.
	other := filepath.Join(dir, "motd")
	first := snap(t, other, "one\n")
	os.WriteFile(other, []byte("two\n"), 0o644)
	if r := sc(t, "restore", first); r.code != 0 || strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("no checker: %+v", r)
	}
}

// An error asks as a blocker does; a problem the file has already is not
// added, so a version with it too restores without a question.
func TestRestoreChecksErrorAndKnown(t *testing.T) {
	_, fstab := checkEnv(t)
	mustSC(t, "init")
	fields := snap(t, fstab, goodLine+"/dev/sdb1 /data2\n") // fstab-fields: an error
	same := snap(t, fstab, badLine+"# the same blocker\n")
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	restoreAnswers(t, "")
	if r := sc(t, "restore", fields); r.code != 2 || !strings.Contains(r.stdout, "adds 1 error compared with") {
		t.Errorf("an error: %+v", r)
	}
	os.WriteFile(fstab, []byte(badLine), 0o644)
	if r := sc(t, "restore", same); r.code != 0 || strings.Contains(r.stdout, "Restore anyway?") {
		t.Errorf("a blocker the file has already: %+v", r)
	}
}

// A file that is not there now: everything the version has counts as
// added, as for a new file in sc edit.
func TestRestoreChecksAbsent(t *testing.T) {
	_, fstab := checkEnv(t)
	mustSC(t, "init")
	bad := snap(t, fstab, badLine)
	os.Remove(fstab)
	restoreAnswers(t, "")
	if r := sc(t, "restore", bad); r.code != 2 || !strings.Contains(r.stdout, "adds 1 blocker compared with "+fstab+" as it is now") {
		t.Errorf("%+v", r)
	}
	if _, err := os.Stat(fstab); err == nil {
		t.Error("restored")
	}
}

// The version is judged by the mode and owner sc restore will write (its
// row's), not the file's on disk now: putting back a world-writable
// sudoers drop-in over a 0440 one asks (M5 follow-up 5a's check.Meta).
func TestRestoreChecksMode(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	mustSC(t, "init")
	d := filepath.Join(t.TempDir(), "sudoers.d")
	os.Mkdir(d, 0o755)
	g, err := check.ParseGraph("check sudoers " + filepath.Join(d, "*") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	t.Cleanup(func() { testHookChecks = nil })
	p := filepath.Join(d, "admins")
	os.WriteFile(p, []byte("owner ALL=(ALL) ALL\n"), 0o440)
	os.Chmod(p, 0o666)
	open := strings.Fields(mustSC(t, "snapshot", p))[1]
	os.Chmod(p, 0o440)
	restoreAnswers(t, "")
	if r := sc(t, "restore", open); r.code != 2 || !strings.Contains(r.stdout, "sudoers-mode") || !strings.Contains(r.stdout, "mode 0666") {
		t.Errorf("%+v", r)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o440 {
		t.Errorf("mode %v", fi.Mode())
	}
}

// Restore's own refusals come before the check and its question: a yes
// that is then refused says nothing true, and a refusal reads the same
// with or without --force (the chunk J review, J5).
func TestRestoreRefusalsFirst(t *testing.T) {
	dir, fstab := checkEnv(t)
	mustSC(t, "init")
	bad := snap(t, fstab, badLine)
	os.WriteFile(fstab, []byte(goodLine), 0o644)

	testHookOpenStore = func(s *store.Store) { s.SetFingerprintOnly(func(p string) bool { return p == fstab }) }
	restoreAnswers(t, "y\n")
	r := sc(t, "restore", bad)
	testHookOpenStore = nil
	if r.code != 1 || !strings.Contains(r.stderr, "fingerprint-only") || strings.Contains(r.stdout, "Restore anyway?") {
		t.Errorf("fingerprint-only: %+v", r)
	}

	// A directory that became a symlink.
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	p := filepath.Join(sub, "fstab")
	g, err := check.ParseGraph("check fstab " + p + "\n")
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	id := snap(t, p, badLine)
	moved := filepath.Join(dir, "moved")
	os.Rename(sub, moved)
	os.Symlink(moved, sub)
	r, f := sc(t, "restore", id), sc(t, "restore", "--force", id)
	if r.code != 1 || r.stderr != f.stderr || !strings.Contains(r.stderr, "(a symlink)") || r.stdout != "" {
		t.Errorf("a directory that is a symlink:\n  without --force: %+v\n  with --force:    %+v", r, f)
	}

	if os.Geteuid() == 0 {
		return // root writes through the mode below
	}
	// A store that can only be read (the rescue console before the
	// remount): refused at once, not asked.
	_, fstab = checkEnv(t)
	mustSC(t, "init")
	bad = snap(t, fstab, badLine)
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	home := os.Getenv("SC_HOME")
	os.Chmod(home, 0o500)
	t.Cleanup(func() { os.Chmod(home, 0o700) })
	for _, answer := range []string{"y\n", ""} {
		restoreAnswers(t, answer)
		if r := sc(t, "restore", bad); r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "may not write") {
			t.Errorf("a read-only store, answer %q: %+v", answer, r)
		}
	}
}

// A check cut short (a validator out of time) does not stop a restore, but
// it is said on stderr: a problem it would have found is neither listed
// nor asked about (the chunk J review, J4).
func TestRestoreNotFullyChecked(t *testing.T) {
	_, fstab := checkEnv(t)
	mustSC(t, "init")
	tools := t.TempDir()
	// Out of time on a file that mentions /slow, clean on any other.
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nfor f; do :; done\nif grep -q /slow \"$f\"; then sleep 30; fi\nexit 0\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs, c.Run.Timeout = []string{tools}, 300*time.Millisecond }
	slow := snap(t, fstab, goodLine+"/dev/null /slow ext4 defaults 0 2\n")
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	restoreAnswers(t, "")
	r := sc(t, "restore", slow)
	if r.code != 0 || strings.Count(r.stderr, "\n") != 1 || !strings.HasPrefix(r.stderr, "sc: note: "+fstab+" not fully checked: ") ||
		!strings.Contains(r.stderr, "did not finish in time") || !strings.HasPrefix(r.stdout, "restored ") {
		t.Errorf("%+v", r)
	}
}

// Whether a path is checked is known without a scratch directory: a file
// no checker reads restores without a note when there is no place for one
// (the chunk J review, J11).
func TestRestoreNoCheckerNoScratch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through the mode of the scratch parent")
	}
	dir, _ := checkEnv(t)
	mustSC(t, "init")
	motd := filepath.Join(dir, "motd")
	first := snap(t, motd, "one\n")
	os.WriteFile(motd, []byte("two\n"), 0o644)
	ro := t.TempDir()
	os.Chmod(ro, 0o500)
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	old := scratchParents
	scratchParents = func() []string { return []string{ro} }
	t.Cleanup(func() { scratchParents = old })
	if r := sc(t, "restore", first); r.code != 0 || r.stderr != "" || !strings.HasPrefix(r.stdout, "restored ") {
		t.Errorf("%+v", r)
	}
}

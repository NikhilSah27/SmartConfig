package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"smartconfig/internal/boot"
	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
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
			!strings.Contains(r.stdout, "\nfstab-source-missing:\n  ") || // the rule's explanation (R16)
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

// The version sc status's undo names, the file as it was at the last
// healthy boot, booted healthy: sc restore writes it with no check and no
// question, even when it has an error of its own, or the file is now
// missing or a link. Any other version is still checked (the chunk J
// review, J1, your pick).
func TestRestoreRescueUndoNotAsked(t *testing.T) {
	dir, fstab, home := statusEnv(t, "BOOT_IMAGE=/vmlinuz ro", false)
	good := snap(t, fstab, goodLine+"/dev/sdb1 /data2\n") // fstab-fields, an error; the boot was healthy
	healthy := time.Now().Add(-time.Hour)
	boot.Seen(home, "aaaaaaaa-1", healthy)
	boot.Record(home, "aaaaaaaa-1", "ok", healthy, newestRow(t), "local-fs=active")
	other := snap(t, fstab, goodLine+"/dev/sdc1 /data3\n") // the same error, recorded after that boot
	snap(t, fstab, badLine)                                // a blocker
	if r := sc(t, "status"); !strings.Contains(r.stdout, "\n  sc restore "+good+"\n") {
		t.Fatalf("sc status names no undo with %s: %+v", good, r)
	}
	real := filepath.Join(dir, "fstab.real")
	for name, now := range map[string]func(){
		"a blocker now": func() { os.WriteFile(fstab, []byte(badLine), 0o644) },
		"missing now":   func() { os.Remove(fstab) },
		"a link now": func() {
			os.WriteFile(real, []byte(badLine), 0o644)
			os.Remove(fstab)
			os.Symlink(real, fstab)
		},
	} {
		now()
		restoreAnswers(t, "") // a question would be answered no
		r := sc(t, "restore", good)
		if r.code != 0 || strings.Contains(r.stdout, "Restore anyway?") || !strings.HasPrefix(r.stdout, "restored "+fstab+" from "+good+" (") {
			t.Errorf("%s: %+v", name, r)
		}
		if fi, err := os.Lstat(fstab); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("%s: %s is not a file now (%v)", name, fstab, err)
		}
	}
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	restoreAnswers(t, "")
	if r := sc(t, "restore", other); r.code != 2 || !strings.Contains(r.stdout, "adds 1 error compared with") || !strings.Contains(r.stdout, "Restore anyway?") {
		t.Errorf("a version after the healthy boot: %+v", r)
	}
}

// Removing a file sc checks, which sc status would then call "error:
// deleted", asks as a version that adds an error does; --force does not,
// nor does a file no checker reads (the chunk J review, J7, your pick).
func TestRestoreRemovalAsks(t *testing.T) {
	dir, fstab := checkEnv(t)
	mustSC(t, "init")
	created(t, fstab, goodLine)
	dne := oldestRow(t, fstab) // its "did not exist" row
	restoreAnswers(t, "")
	r := sc(t, "restore", dne)
	if r.code != 2 || r.stdout != "Restoring "+dne+" removes "+fstab+", which sc checks (fstab): sc status would call that an error.\n"+
		"Restore anyway? [y/N]: \nnot restored: "+fstab+" is unchanged\n" {
		t.Errorf("no answer: %+v", r)
	}
	oldTerm := atTerminal
	atTerminal = func(io.Reader, io.Writer) bool { return false }
	r = sc(t, "restore", dne)
	atTerminal = oldTerm
	if r.code != 2 || r.stderr != "sc: not restored: "+fstab+" is unchanged (no terminal to ask at; sc restore --force "+dne+" removes it)\n" {
		t.Errorf("no terminal: %+v", r)
	}
	if _, err := os.Stat(fstab); err != nil {
		t.Fatalf("removed without a yes: %v", err)
	}
	restoreAnswers(t, "y\n")
	if r := sc(t, "restore", dne); r.code != 0 || !strings.Contains(r.stdout, "[y/N]: removed "+fstab+" to match "+dne) {
		t.Errorf("y: %+v", r)
	}
	if _, err := os.Stat(fstab); err == nil {
		t.Error("y did not remove it")
	}
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	restoreAnswers(t, "")
	if r := sc(t, "restore", "--force", dne); r.code != 0 || !strings.HasPrefix(r.stdout, "removed "+fstab) {
		t.Errorf("--force: %+v", r)
	}
	motd := filepath.Join(dir, "motd")
	created(t, motd, "hello\n")
	other := oldestRow(t, motd)
	if r := sc(t, "restore", other); r.code != 0 || !strings.HasPrefix(r.stdout, "removed "+motd) {
		t.Errorf("a file no checker reads: %+v", r)
	}
}

// oldestRow is the id of p's oldest row, from sc log: after created, its
// "did not exist" row.
func oldestRow(t *testing.T, p string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(mustSC(t, "log", "-n", "0", p)), "\n")
	return strings.Fields(lines[len(lines)-1])[0]
}

// The answers to restore's question: y or yes, in any case, with spaces
// or a CR around it, or with no newline at the end of input; anything
// else, an empty line and end of input are no (kept from the chunk J
// review's tests: its mutations R7 and N3 survived the suite).
func TestRestoreAnswers(t *testing.T) {
	_, fstab := checkEnv(t)
	mustSC(t, "init")
	bad := snap(t, fstab, badLine)
	for _, tc := range []struct {
		answer string
		code   int
	}{
		{"yes\n", 0}, {" Y \n", 0}, {"y\r\n", 0}, {"y", 0}, {"YES", 0},
		{"yess\n", 2}, {"ye\n", 2}, {"", 2}, {"\n", 2}, {"no\ny\n", 2},
	} {
		os.WriteFile(fstab, []byte(goodLine), 0o644)
		restoreAnswers(t, tc.answer)
		r := sc(t, "restore", bad)
		if r.code != tc.code {
			t.Errorf("answer %q: exit %d, want %d: %+v", tc.answer, r.code, tc.code, r)
		}
		if b, _ := os.ReadFile(fstab); (tc.code == 0) != (string(b) == badLine) {
			t.Errorf("answer %q: file %q", tc.answer, b)
		}
	}
}

// What restore does not check: a link row, which writes no content; and
// on a read-only root, as on the rescue console, no validator runs, only
// sc's own rules, which still ask (the chunk J review's R1 and R3).
func TestRestoreUnchecked(t *testing.T) {
	dir, fstab := checkEnv(t)
	mustSC(t, "init")
	tools := t.TempDir()
	ran := filepath.Join(t.TempDir(), "ran")
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
	target := filepath.Join(dir, "elsewhere")
	os.WriteFile(target, []byte(badLine), 0o644)
	os.Symlink(target, fstab)
	link := strings.Fields(mustSC(t, "snapshot", fstab))[1]
	os.Remove(fstab)
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	mustSC(t, "snapshot", fstab)
	restoreAnswers(t, "")
	if r := sc(t, "restore", link); r.code != 0 || !strings.Contains(r.stdout, "link -> ") {
		t.Errorf("a link row: %+v", r)
	}
	os.Remove(fstab)
	bad := snap(t, fstab, badLine)
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	os.Remove(ran)
	old := rootReadOnly
	rootReadOnly = func() bool { return true }
	t.Cleanup(func() { rootReadOnly = old })
	if r := sc(t, "restore", bad); r.code != 2 || !strings.Contains(r.stdout, "fstab-source-missing") {
		t.Errorf("a read-only root: %+v", r)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a validator ran on a read-only root")
	}
}

// A check that cannot run does not stop a restore: no place for a
// scratch directory, or a check of the file now or of the version that
// fails, gives one note on stderr, and the version is written (the chunk
// J review's R11, R12 and R13).
func TestRestoreCheckCannotRun(t *testing.T) {
	dir, fstab := checkEnv(t)
	mustSC(t, "init")
	bad := snap(t, fstab, badLine)
	note := "sc: note: " + fstab + " not checked ("
	// A check fails: its scratch copies go under a regular file.
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, nil, 0o644)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Home = filepath.Join(plain, "home") }
	for name, now := range map[string]func(){
		"the file now": func() { os.WriteFile(fstab, []byte(goodLine), 0o644) },
		"the version":  func() { os.Remove(fstab) }, // only the version is checked
	} {
		now()
		restoreAnswers(t, "")
		r := sc(t, "restore", bad)
		if r.code != 0 || !strings.HasPrefix(r.stderr, note) || strings.Count(r.stderr, "\n") != 1 || !strings.HasPrefix(r.stdout, "restored ") {
			t.Errorf("a check of %s fails: %+v", name, r)
		}
	}
	testHookChecks = hook
	if os.Geteuid() == 0 {
		return // root writes through the mode below
	}
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	os.RemoveAll(filepath.Join(os.Getenv("SC_HOME"), "tmp"))
	ro := t.TempDir()
	os.Chmod(ro, 0o500)
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	oldParents := scratchParents
	scratchParents = func() []string { return []string{ro} }
	t.Cleanup(func() { scratchParents = oldParents })
	restoreAnswers(t, "")
	if r := sc(t, "restore", bad); r.code != 0 || !strings.HasPrefix(r.stderr, note+"no scratch directory") || strings.Count(r.stderr, "\n") != 1 {
		t.Errorf("no scratch directory: %+v", r)
	}
}

// ownedRow records p's content as a row with the given mode, owner and
// group, which a test cannot give a file without root: restore only
// checks and asks about it here, and never writes it.
func ownedRow(t *testing.T, p string, mode os.FileMode, uid, gid int) string {
	t.Helper()
	st, err := fsutil.ReadState(p)
	if err != nil {
		t.Fatal(err)
	}
	st.Meta.Mode, st.Meta.UID, st.Meta.GID = mode, uid, gid
	s, err := store.Open(os.Getenv("SC_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Record([]store.Obs{{Path: p, State: &st, Force: true, Origin: store.OriginAuto}})
	if err != nil || len(res) != 1 || !res[0].Recorded {
		t.Fatalf("record %s: %v %+v", p, err, res)
	}
	return res[0].Change.ID
}

// The version is judged by the owner and group of its row, which restore
// writes: a sudoers drop-in owned by another user, or group-writable by a
// group other than root's, asks (the chunk J review's R17 and R20).
func TestRestoreChecksOwner(t *testing.T) {
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
	os.Chmod(p, 0o440)
	for name, id := range map[string]string{
		"another owner":          ownedRow(t, p, 0o440, 4242, 0),
		"a group that can write": ownedRow(t, p, 0o460, 0, 4242),
	} {
		restoreAnswers(t, "")
		if r := sc(t, "restore", id); r.code != 2 || !strings.Contains(r.stdout, "sudoers-mode") || !strings.Contains(r.stdout, "Restore anyway?") {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

// A version that adds only a warning is restored with no question (the
// chunk J review's N6).
func TestRestoreWarningNotAsked(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	mustSC(t, "init")
	hosts := filepath.Join(t.TempDir(), "hosts")
	g, err := check.ParseGraph("check hosts " + hosts + "\n")
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	t.Cleanup(func() { testHookChecks = nil })
	v := snap(t, hosts, "10.0.0.1 db\n") // no localhost: a warning
	os.WriteFile(hosts, []byte("127.0.0.1 localhost\n10.0.0.1 db\n"), 0o644)
	restoreAnswers(t, "")
	if r := sc(t, "restore", v); r.code != 0 || strings.Contains(r.stdout, "Restore anyway?") || !strings.HasPrefix(r.stdout, "restored ") {
		t.Errorf("%+v", r)
	}
}

// Removals that do not ask: a flag file, whose own finding asks for its
// removal; a unit that one of the same name in a later unit directory
// stands in for; and a path already absent, where there is nothing to
// remove (the chunk J review's J7, as statusProblem judges them).
func TestRestoreRemovalNotAsked(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	mustSC(t, "init")
	dir := t.TempDir()
	etc, lib := filepath.Join(dir, "etc/systemd/system"), filepath.Join(dir, "lib/systemd/system")
	os.MkdirAll(etc, 0o755)
	os.MkdirAll(lib, 0o755)
	oldEtc, oldLater := unitEtcDir, unitLaterDirs
	unitEtcDir, unitLaterDirs = etc, []string{lib}
	t.Cleanup(func() { unitEtcDir, unitLaterDirs = oldEtc, oldLater })
	nologin, unit, fstab := filepath.Join(dir, "nologin"), filepath.Join(etc, "x.service"), filepath.Join(dir, "fstab")
	g, err := check.ParseGraph("check flag " + nologin + "\ncheck unit " + etc + "/*.service\ncheck fstab " + fstab + "\n")
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	t.Cleanup(func() { testHookChecks = nil })
	os.WriteFile(filepath.Join(lib, "x.service"), []byte("[Service]\nExecStart=/bin/true\n"), 0o644)
	for _, p := range []string{nologin, unit} {
		created(t, p, "[Service]\nExecStart=/bin/true\n")
		restoreAnswers(t, "")
		if r := sc(t, "restore", oldestRow(t, p)); r.code != 0 || !strings.HasPrefix(r.stdout, "removed "+p) {
			t.Errorf("%s: %+v", p, r)
		}
	}
	created(t, fstab, goodLine)
	os.Remove(fstab)
	restoreAnswers(t, "")
	if r := sc(t, "restore", oldestRow(t, fstab)); r.code != 0 || !strings.HasPrefix(r.stdout, "nothing to do: "+fstab+" is already absent") {
		t.Errorf("already absent: %+v", r)
	}
}

// TestRestoreHelper is sc itself, run by TestRestoreCtrlCAtQuestion as a
// child with a graph, and as at a terminal.
func TestRestoreHelper(t *testing.T) {
	if os.Getenv("SC_RESTORE_HELPER") == "" {
		t.Skip("only as a child of TestRestoreCtrlCAtQuestion")
	}
	g, err := check.ParseGraph(os.Getenv("SC_RESTORE_GRAPH"))
	if err != nil {
		os.Exit(99)
	}
	empty := os.Getenv("SC_RESTORE_EMPTY")
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	atTerminal = func(io.Reader, io.Writer) bool { return true }
	os.Args = append([]string{"sc"}, strings.Fields(os.Getenv("SC_RESTORE_ARGS"))...)
	main()
}

// Ctrl-C at restore's question ends sc at once by the signal: nothing is
// written, no row is added, and the check's private directory is gone.
// Only the write is a mutating command (the chunk J review's R9: with
// mutating set before the store opened, sc waited for a write that never
// came; kept from its tests).
func TestRestoreCtrlCAtQuestion(t *testing.T) {
	_, fstab := checkEnv(t)
	mustSC(t, "init")
	bad := snap(t, fstab, badLine)
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	rows := func() int { return strings.Count(mustSC(t, "log", "-n", "0", fstab), "\n") }
	n := rows()
	os.RemoveAll(filepath.Join(os.Getenv("SC_HOME"), "tmp")) // the scratch directory goes to TMPDIR, then
	tmp, xdg := t.TempDir(), t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRestoreHelper$")
	cmd.Env = append(os.Environ(), "SC_RESTORE_HELPER=1", "SC_RESTORE_GRAPH=check fstab "+fstab+"\n",
		"SC_RESTORE_EMPTY="+t.TempDir(), "SC_RESTORE_ARGS=restore "+bad, "TMPDIR="+tmp, "XDG_RUNTIME_DIR="+xdg)
	stdin, _ := cmd.StdinPipe()
	defer stdin.Close()
	stdout, _ := cmd.StdoutPipe()
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	asked := make(chan bool, 1)
	go func() {
		rd := bufio.NewReader(stdout)
		var seen strings.Builder
		for {
			b, err := rd.ReadByte()
			if err != nil {
				asked <- false
				return
			}
			seen.WriteByte(b)
			if strings.HasSuffix(seen.String(), "Restore anyway? [y/N]: ") {
				asked <- true
				return
			}
		}
	}()
	select {
	case ok := <-asked:
		if !ok {
			t.Fatalf("no question; stderr %q", stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no question in 20 s")
	}
	left := func() []string {
		var out []string
		for _, d := range []string{tmp, xdg} {
			es, _ := os.ReadDir(d)
			for _, e := range es {
				out = append(out, filepath.Join(d, e.Name()))
			}
		}
		return out
	}
	if l := left(); len(l) != 1 {
		t.Errorf("want the check's one private directory while it asks, got %v", l)
	}
	cmd.Process.Signal(syscall.SIGINT)
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var err error
	select {
	case err = <-waited:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("Ctrl-C at the question did not end sc in 10 s; stderr %q", stderr.String())
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("ended with %v, stderr %q", err, stderr.String())
	}
	if ws := ee.Sys().(syscall.WaitStatus); !ws.Signaled() || ws.Signal() != syscall.SIGINT || stderr.String() != "sc: interrupted by interrupt\n" {
		t.Errorf("end: %v, stderr %q", ws, stderr.String())
	}
	if b, _ := os.ReadFile(fstab); string(b) != goodLine || rows() != n {
		t.Errorf("changed: %q, %d rows (was %d)", b, rows(), n)
	}
	if l := left(); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"bytes"
	"smartconfig/internal/boot"
	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
	"syscall"
	"unsafe"
)

// statusEnv is checkEnv (a graph whose fstab is dir/fstab, no validators)
// with this boot's id, its kernel command line, whether / is read-only,
// and no scd. It returns the fstab and the store's home.
func statusEnv(t *testing.T, cmdline string, ro bool) (dir, fstab, home string) {
	t.Helper()
	dir, fstab = checkEnv(t)
	home = os.Getenv("SC_HOME")
	mustSC(t, "init")
	p := filepath.Join(t.TempDir(), "boot_id")
	os.WriteFile(p, []byte("cccccccc-0000-0000-0000-000000000003\n"), 0o644)
	cl := filepath.Join(t.TempDir(), "cmdline")
	os.WriteFile(cl, []byte(cmdline+"\n"), 0o644)
	oldID, oldCL, oldRO, oldProc, oldRunner := boot.IDPath, cmdlinePath, rootReadOnly, procDir, bootRunner
	boot.IDPath, cmdlinePath, procDir = p, cl, t.TempDir()
	rootReadOnly = func() bool { return ro }
	bootRunner.Dirs = []string{t.TempDir()} // no systemctl: the mode is the command line's
	t.Cleanup(func() {
		boot.IDPath, cmdlinePath, rootReadOnly, procDir, bootRunner = oldID, oldCL, oldRO, oldProc, oldRunner
	})
	return dir, fstab, home
}

// snap writes data to p and records it, returning the row's id.
func snap(t *testing.T, p, data string) string {
	t.Helper()
	os.WriteFile(p, []byte(data), 0o644)
	out := mustSC(t, "snapshot", p)
	f := strings.Fields(out)
	if len(f) < 2 {
		t.Fatalf("snapshot printed %q", out)
	}
	return f[1]
}

// newestRow is the store's newest row.
func newestRow(t *testing.T) int64 {
	t.Helper()
	s, err := store.Open(os.Getenv("SC_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, _ := s.NewestRowID()
	return n
}

// created writes data to p and records it as scd records a file it saw
// being created: "did not exist", then the file. It returns the file's id.
func created(t *testing.T, p, data string) string {
	t.Helper()
	os.WriteFile(p, []byte(data), 0o644)
	st, err := fsutil.ReadState(p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(os.Getenv("SC_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Record([]store.Obs{{Path: p, State: &st, Created: true, Origin: store.OriginAuto}})
	if err != nil || len(res) != 1 || !res[0].Recorded {
		t.Fatalf("record %s: %v %+v", p, err, res)
	}
	return res[0].Change.ID
}

const goodLine, badLine = "/dev/null /data ext4 defaults 0 2\n", "/dev/sc-no-such-disk /data ext4 defaults 0 2\n"

// After a healthy boot, a change that added a blocker, and a boot that
// never reached multi-user: sc status names the boots, lists what changed
// with the problem each change added, gives the commands to undo the
// newest blocker, and exits 2.
func TestStatusAfterFailedBoot(t *testing.T) {
	dir, fstab, home := statusEnv(t, "BOOT_IMAGE=/vmlinuz root=UUID=x ro quiet splash", false)
	good := snap(t, fstab, goodLine)
	healthy := time.Now().Add(-time.Hour)
	boot.Seen(home, "aaaaaaaa-1", healthy)
	boot.Record(home, "aaaaaaaa-1", "ok", healthy, newestRow(t), "local-fs=active")
	bad := snap(t, fstab, badLine)
	other := filepath.Join(dir, "motd")
	snap(t, other, "hello\n")
	boot.Seen(home, "bbbbbbbb-2", time.Now().Add(-time.Minute))

	r := sc(t, "status")
	if r.code != 2 || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		"This boot:     cccccccc (normal), root read-write\n",
		"Last healthy:  " + when(healthy.Truncate(time.Second)) + ", boot aaaaaaaa\n",
		"Failed since:  1 boot, last ",
		": never reached multi-user\n",
		"scd:           not running\n",
		"\nChanged since the last healthy boot, newest first:\n",
		bad + "  ",
		"blocker fstab-source-missing, line 1\n",
		"\nTo put " + fstab + " back as it was during the last healthy boot:\n  sc restore " + good + "\n  sync\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "remount") || strings.Contains(r.stdout, "systemctl reboot") {
		t.Errorf("rescue steps on a normal boot:\n%s", r.stdout)
	}
	// motd, the newest change, comes first; it has no checker.
	if !strings.Contains(r.stdout, other+"   -\n") {
		t.Errorf("motd's line:\n%s", r.stdout)
	}
	if strings.Index(r.stdout, other) > strings.Index(r.stdout, bad) {
		t.Errorf("not newest first:\n%s", r.stdout)
	}
}

// The rescue console fits an 80x25 screen with systemd's five lines:
// worst first, at most consoleRows files, the rest counted, long paths
// shortened from the left (but never in a command); every change is still
// judged, and the undo has the remount, daemon-reload and reboot, and says
// the menu shows once more.
func TestStatusConsole(t *testing.T) {
	dir, fstab, home := statusEnv(t, "root=UUID=x ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1", true)
	good := snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)
	for i := 0; i < 21; i++ {
		snap(t, filepath.Join(dir, fmt.Sprintf("f%02d", i)), "x\n")
	}
	// A unit enabled meanwhile: a symlink to a long target.
	unit := filepath.Join(dir, "cups-browsed.service")
	os.Symlink("/usr/lib/systemd/system/cups-browsed-with-a-long-name.service", unit)
	mustSC(t, "snapshot", unit)
	boot.Record(home, "bbbbbbbb-2", "bad", time.Now(), newestRow(t), "local-fs=inactive emergency=active rescue=inactive failed-units=0")
	r := sc(t, "status", "--console")
	if r.code != 2 {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	// 20 screen rows, whatever wraps: systemd's prompt takes the other 5.
	if n := screenRows(r.stdout); n > 20 {
		t.Errorf("%d screen rows:\n%s", n, r.stdout)
	}
	for _, want := range []string{
		"This boot:     cccccccc (rescue), root read-only\n",
		"Failed since:  1 boot, last ", ": a mount failed, emergency mode\n",
		"\nChanged since the last healthy boot, worst first:\n",
		"blocker fstab-source-missing, line 1  ",
		"18 files more (sc status lists them all)\n",
		" back:\n  mount -o remount,rw /\n  sc restore " + good + "\n  sync\n  systemctl daemon-reload\n  systemctl reboot\n" +
			"The menu shows once more: the first entry, Ubuntu, is the one.\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("lacks %q:\n%s", want, r.stdout)
		}
	}
	// Worst first: the blocker is the first file listed, its path cut short.
	if !strings.Contains(lines[6], "blocker") || !strings.HasSuffix(lines[6], "/fstab") {
		t.Errorf("not worst first:\n%s", r.stdout)
	}
}

// To /dev/console (the drop-in), the report goes to every console the
// kernel uses, as sulogin asks on each; elsewhere to the command's output.
func TestStatusAllConsoles(t *testing.T) {
	_, fstab, home := statusEnv(t, "systemd.unit=rescue.target", true)
	snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	devs := t.TempDir()
	active := filepath.Join(t.TempDir(), "active")
	os.WriteFile(active, []byte("tty1 ttyS0\n"), 0o644)
	for _, n := range []string{"tty1", "ttyS0"} {
		os.WriteFile(filepath.Join(devs, n), nil, 0o644)
	}
	oldActive, oldDev, oldSys := consoleActive, devDir, systemConsole
	consoleActive, devDir = active, devs
	t.Cleanup(func() { consoleActive, devDir, systemConsole = oldActive, oldDev, oldSys })
	systemConsole = func(io.Writer) bool { return true }
	r := sc(t, "status", "--console")
	tty1, _ := os.ReadFile(filepath.Join(devs, "tty1"))
	ttyS0, _ := os.ReadFile(filepath.Join(devs, "ttyS0"))
	if r.stdout != "" || !strings.HasPrefix(string(tty1), "This boot:") || string(tty1) != string(ttyS0) {
		t.Errorf("stdout %q, tty1 %q, ttyS0 %q", r.stdout, tty1, ttyS0)
	}
	systemConsole = func(io.Writer) bool { return false }
	if r := sc(t, "status", "--console"); !strings.HasPrefix(r.stdout, "This boot:") {
		t.Errorf("a terminal: %+v", r)
	}
}

// The console runs sc's own rules only: a validator that would take long
// (findmnt on a dying disk) cannot hide a blocker, nor hang the console.
func TestStatusConsoleOwnRules(t *testing.T) {
	_, fstab, home := statusEnv(t, "systemd.unit=rescue.target", true)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
	snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)
	start := time.Now()
	r := sc(t, "status", "--console")
	if r.code != 2 || !strings.Contains(r.stdout, "blocker fstab-source-missing") || time.Since(start) > 5*time.Second {
		t.Fatalf("%v: %+v", time.Since(start), r)
	}
}

// The healthy boot after a fix: this boot's ok verdict makes it the last
// healthy boot, the failed boot before it is past, and only what changed
// since this boot came up is listed (the chunk B review: a healthy
// machine said exit 2).
func TestStatusAfterFix(t *testing.T) {
	_, fstab, home := statusEnv(t, "ro", false)
	snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)
	boot.Seen(home, "bbbbbbbb-2", time.Now())
	snap(t, fstab, goodLine) // restored
	boot.Seen(home, "cccccccc-0000-0000-0000-000000000003", time.Now())
	boot.Record(home, "cccccccc-0000-0000-0000-000000000003", "ok", time.Now(), newestRow(t), "local-fs=active")
	r := sc(t, "status")
	if r.code != 0 || !strings.Contains(r.stdout, "Last healthy:  this boot, ") || strings.Contains(r.stdout, "Failed since") ||
		!strings.HasSuffix(r.stdout, "\nNothing recorded has changed since this boot came up.\n") {
		t.Fatalf("%+v", r)
	}
	// A change made since this boot came up is judged against this boot.
	snap(t, fstab, badLine)
	if r := sc(t, "status"); r.code != 2 || !strings.Contains(r.stdout, "\nChanged since this boot came up, newest first:\n") {
		t.Errorf("%+v", r)
	}
}

// A boot that failed since the last healthy one is reason enough for exit
// 2, also with nothing changed; a boot that was only seen failed, unless it
// is this one.
func TestStatusFailedBootAlone(t *testing.T) {
	_, fstab, home := statusEnv(t, "ro", false)
	snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	boot.Seen(home, "cccccccc-0000-0000-0000-000000000003", time.Now()) // this boot, on its way
	if r := sc(t, "status"); r.code != 0 || strings.Contains(r.stdout, "Failed since") {
		t.Errorf("this boot counted as failed: %+v", r)
	}
	boot.Seen(home, "bbbbbbbb-2", time.Now())
	if r := sc(t, "status"); r.code != 2 || !strings.Contains(r.stdout, "Failed since:  1 boot") ||
		!strings.Contains(r.stdout, "Nothing recorded has changed since the last healthy boot.") {
		t.Errorf("%+v", r)
	}
}

// A verdict given while the store could not be read has no row: the
// changes are counted from the healthy boot before it.
func TestStatusVerdictWithoutRow(t *testing.T) {
	_, fstab, home := statusEnv(t, "ro", false)
	good := snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)
	boot.Record(home, "bbbbbbbb-2", "ok", time.Now(), -1, "local-fs=active")
	r := sc(t, "status")
	if r.code != 2 || !strings.Contains(r.stdout, "boot bbbbbbbb") || !strings.Contains(r.stdout, "sc restore "+good) {
		t.Errorf("%+v", r)
	}
}

// Without a healthy boot, a file changed several times is compared with
// its oldest version listed: a blocker an earlier edit added is found.
// At most ten rows are listed.
func TestStatusNoBootsEdits(t *testing.T) {
	dir, fstab, _ := statusEnv(t, "ro", false)
	good := snap(t, fstab, goodLine)
	snap(t, fstab, badLine)
	snap(t, fstab, "# a comment\n"+badLine)
	r := sc(t, "status")
	if r.code != 2 || !strings.Contains(r.stdout, "blocker fstab-source-missing") || !strings.Contains(r.stdout, "sc restore "+good) {
		t.Errorf("%+v", r)
	}
	for i := 0; i < 12; i++ {
		snap(t, filepath.Join(dir, fmt.Sprintf("f%02d", i)), "x\n")
	}
	if r := sc(t, "status"); strings.Count(r.stdout, filepath.Join(dir, "f")) != 10 {
		t.Errorf("not ten rows:\n%s", r.stdout)
	}
}

// A new file with a blocker is undone by moving it aside; a checked file
// that was deleted is an error, undone by sc restore.
func TestStatusNewAndDeleted(t *testing.T) {
	_, fstab, home := statusEnv(t, "systemd.unit=emergency.target", false)
	other := filepath.Join(filepath.Dir(fstab), "other")
	snap(t, other, "x\n")
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	created(t, fstab, badLine) // new since
	r := sc(t, "status")
	if r.code != 2 || !strings.Contains(r.stdout, "\nTo undo "+fstab+", which is new, move it aside:\n  mv "+fstab+" "+fstab+".sc-off\n  sync\n  systemctl daemon-reload\n  systemctl reboot\n") ||
		!strings.Contains(r.stdout, "(emergency)") {
		t.Errorf("new: %+v", r)
	}

	_, fstab, home = statusEnv(t, "ro", false)
	good := snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(fstab)
	s.Record([]store.Obs{{Path: fstab, Origin: store.OriginAuto}})
	s.Close()
	r = sc(t, "status")
	if r.code != 2 || !strings.Contains(r.stdout, "error: deleted") || !strings.Contains(r.stdout, "  sc restore "+good+"\n") {
		t.Errorf("deleted: %+v", r)
	}
}

// Without a healthy boot, the newest rows are compared with the version
// before the oldest of them: the edit that added the blocker may be that
// oldest row, its good version older than the list (the M4 final review,
// B2: "no problem found", no undo).
func TestStatusNoHealthyBoot(t *testing.T) {
	dir, fstab, home := statusEnv(t, "ro", false)
	good := snap(t, fstab, goodLine)
	snap(t, fstab, badLine)
	for i := 0; i < 8; i++ {
		snap(t, filepath.Join(dir, fmt.Sprintf("f%02d", i)), "x\n")
	}
	snap(t, fstab, "# a comment\n"+badLine)
	boot.Seen(home, "bbbbbbbb-2", time.Now().Add(-time.Minute))
	for _, args := range [][]string{{"status"}, {"status", "--console"}} {
		r := sc(t, args...)
		if r.code != 2 || !strings.Contains(r.stdout, "blocker fstab-source-missing, line 2") || !strings.Contains(r.stdout, "  sc restore "+good+"\n") {
			t.Errorf("%v: %+v", args, r)
		}
		if len(args) == 2 && !strings.Contains(r.stdout, "\nThe newest changes, worst first (no healthy boot is recorded):\n") {
			t.Errorf("%v: title:\n%s", args, r.stdout)
		}
	}
}

// A path with no version before the line is new only with proof of
// absence ("did not exist"). One first seen after it (scd's baseline, a
// snapshot) is compared with its first version, and with only that one,
// gets no undo: moving fstab aside was the undo (the M4 final review, B2).
func TestStatusFirstSeenIsNotNew(t *testing.T) {
	_, fstab, home := statusEnv(t, "ro fstab=no systemd.unit=rescue.target", true)
	snap(t, fstab, goodLine+badLine) // fstab's first row: scd's baseline
	boot.Seen(home, "bbbbbbbb-2", time.Now().Add(-time.Minute))
	for _, args := range [][]string{{"status"}, {"status", "--console"}} {
		r := sc(t, args...)
		if r.code != 2 || !strings.Contains(r.stdout, "blocker fstab-source-missing") || strings.Contains(r.stdout, "mv ") ||
			!strings.Contains(r.stdout, "is recorded: fix it by hand.\n") || strings.Contains(r.stdout, "remount") {
			t.Errorf("no healthy boot, %v: %+v", args, r)
		}
	}

	// A healthy verdict given before the baseline reached fstab (row 0,
	// or scd still starting): fstab first seen good after the line, then
	// the bad edit. The first version is the one to put back.
	_, fstab, home = statusEnv(t, "ro fstab=no systemd.unit=rescue.target", true)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	first := snap(t, fstab, goodLine)
	snap(t, fstab, badLine)
	for _, args := range [][]string{{"status"}, {"status", "--console"}} {
		r := sc(t, args...)
		if r.code != 2 || !strings.Contains(r.stdout, "blocker fstab-source-missing") || !strings.Contains(r.stdout, "  sc restore "+first+"\n") || strings.Contains(r.stdout, "mv ") {
			t.Errorf("first seen after the line, %v: %+v", args, r)
		}
	}
}

// A flag file deleted since the healthy boot is the fix its own finding
// asks for, not an error with an undo that puts it back (the M4 final
// review, B3); fstab deleted still is one.
func TestStatusDeletedFlag(t *testing.T) {
	dir, _, home := statusEnv(t, "ro", false)
	flag := filepath.Join(dir, "sshd_not_to_be_run")
	g, err := check.ParseGraph("check flag " + flag + "\n")
	if err != nil {
		t.Fatal(err)
	}
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Graph = g }
	t.Cleanup(func() { testHookChecks = hook })
	snap(t, flag, "")
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(flag)
	s.Record([]store.Obs{{Path: flag, Origin: store.OriginAuto}})
	s.Close()
	if r := sc(t, "status"); r.code != 0 || !strings.Contains(r.stdout, "  deleted\n") || strings.Contains(r.stdout, "sc restore") {
		t.Errorf("%+v", r)
	}
}

// scd is a plain sc watch; a user's sc watch --root is not it.
func TestStatusScdState(t *testing.T) {
	statusEnv(t, "ro", false)
	for _, tc := range []struct{ cmdline, want string }{
		{"/usr/local/sbin/sc\x00watch\x00", "running (pid 4242)"},
		{"sc\x00watch\x00--root\x00/home/u/x\x00", "not running"},
		{"/usr/bin/vim\x00sc\x00watch\x00", "not running"},
	} {
		os.RemoveAll(filepath.Join(procDir, "4242"))
		os.MkdirAll(filepath.Join(procDir, "4242"), 0o755)
		os.WriteFile(filepath.Join(procDir, "4242", "cmdline"), []byte(tc.cmdline), 0o644)
		if got := scdState(); got != tc.want {
			t.Errorf("%q: %s", tc.cmdline, got)
		}
	}
}

// With no boot recorded (the units are not installed), sc status lists
// the newest changes, each judged against the version before it.
func TestStatusNoBoots(t *testing.T) {
	_, fstab, _ := statusEnv(t, "ro", false)
	good := snap(t, fstab, goodLine)
	snap(t, fstab, badLine)
	r := sc(t, "status")
	if r.code != 2 || !strings.Contains(r.stdout, "Last healthy:  none recorded") ||
		!strings.Contains(r.stdout, "\nThe newest changes (no healthy boot is recorded), newest first:\n") ||
		!strings.Contains(r.stdout, "\nTo put "+fstab+" back as it was before this change:\n  sc restore "+good+"\n") {
		t.Fatalf("%+v", r)
	}
}

// Nothing changed since a healthy boot: exit 0, one line says so.
func TestStatusHealthy(t *testing.T) {
	_, fstab, home := statusEnv(t, "ro", false)
	snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	r := sc(t, "status")
	if r.code != 0 || !strings.HasSuffix(r.stdout, "\nNothing recorded has changed since the last healthy boot.\n") || strings.Contains(r.stdout, "Failed since") {
		t.Fatalf("%+v", r)
	}
}

// A file changed twice since the healthy boot is judged against the
// healthy boot's version, not the one before its newest change; with two
// blockers, the undo is for the newest change.
func TestStatusTwoEditsSinceHealthy(t *testing.T) {
	dir, fstab, home := statusEnv(t, "ro", false)
	fstab2 := filepath.Join(dir, "fstab2")
	g, err := check.ParseGraph("check fstab " + fstab + " " + fstab2 + "\n")
	if err != nil {
		t.Fatal(err)
	}
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Graph = g }
	good := snap(t, fstab, goodLine)
	good2 := snap(t, fstab2, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab2, badLine) // the older blocker
	snap(t, fstab, badLine)
	snap(t, fstab, "# only a comment more\n"+badLine) // the newest change adds nothing itself
	r := sc(t, "status")
	if r.code != 2 || strings.Count(r.stdout, "blocker fstab-source-missing") != 2 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.stdout, "\nTo put "+fstab+" back as it was during the last healthy boot:\n  sc restore "+good+"\n") ||
		strings.Contains(r.stdout, "sc restore "+good2) {
		t.Errorf("undo:\n%s", r.stdout)
	}
}

// An older blocker keeps the undo when a newer change adds only an error:
// the console lists the blocker first, and its commands must put that
// file back, not the newer one (the chunk E review).
func TestStatusBlockerBeforeNewerError(t *testing.T) {
	dir, fstab, home := statusEnv(t, "ro", false)
	fstab2 := filepath.Join(dir, "fstab2")
	g, err := check.ParseGraph("check fstab " + fstab + " " + fstab2 + "\n")
	if err != nil {
		t.Fatal(err)
	}
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Graph = g }
	good := snap(t, fstab, goodLine)
	good2 := snap(t, fstab2, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)                    // the older blocker
	snap(t, fstab2, goodLine+"/dev/null /x\n") // the newer error: two fields
	for _, args := range [][]string{{"status"}, {"status", "--console"}} {
		r := sc(t, args...)
		if r.code != 2 || !strings.Contains(r.stdout, "blocker fstab-source-missing") || !strings.Contains(r.stdout, "error fstab-fields") {
			t.Fatalf("%v: %+v", args, r)
		}
		if !strings.Contains(r.stdout, "\n  sc restore "+good+"\n") || strings.Contains(r.stdout, "sc restore "+good2) {
			t.Errorf("%v: the undo is not the blocker's:\n%s", args, r.stdout)
		}
	}
	// With the blocker fixed, the error gets the undo.
	snap(t, fstab, goodLine)
	if r := sc(t, "status"); r.code != 2 || !strings.Contains(r.stdout, "\n  sc restore "+good2+"\n") {
		t.Errorf("the error alone: %+v", r)
	}
}

// Long paths and a symlink to a long target still fit: the problem column
// is cut, a symlink's target left out, a new file's undo is cd and a short
// mv. The menu is promised again only while its flag is set: after a
// failed boot, not when the rescue entry was picked by hand after a
// healthy boot, nor when this boot came up healthy (the chunk E review).
func TestStatusConsoleFits(t *testing.T) {
	dir, fstab, home := statusEnv(t, "systemd.unit=rescue.target", true)
	for i := 0; i < 6; i++ {
		long := filepath.Join(dir, strings.Repeat("d", 30), fmt.Sprintf("getty@tty%d.service.d", i))
		os.MkdirAll(long, 0o755)
		snap(t, filepath.Join(long, "override.conf"), "x\n")
	}
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	for i := 0; i < 6; i++ {
		l := filepath.Join(dir, fmt.Sprintf("very-long-unit-name-number-%d.service", i))
		os.Symlink("/usr/lib/systemd/system/a-very-long-unit-name-indeed-"+fmt.Sprint(i)+".service", l)
		mustSC(t, "snapshot", l)
	}
	created(t, fstab, badLine) // new since the healthy boot
	// The rescue entry picked by hand after the healthy boot: no flag.
	if r := sc(t, "status", "--console"); r.code != 2 || strings.Contains(r.stdout, "The menu shows once more") {
		t.Errorf("rescue after a healthy boot: %+v", r)
	}
	boot.Seen(home, "bbbbbbbb-2", time.Now()) // a boot that never reached multi-user
	r := sc(t, "status", "--console")
	if r.code != 2 || screenRows(r.stdout) > 20 {
		t.Fatalf("%d rows: %+v", screenRows(r.stdout), r)
	}
	for _, want := range []string{"now a symlink  ", "  cd " + dir + "/\n  mv fstab fstab.sc-off\n", "The menu shows once more"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("lacks %q:\n%s", want, r.stdout)
		}
	}
	// This boot came up healthy: rescue entered by hand, no menu promised.
	boot.Record(home, "cccccccc-0000-0000-0000-000000000003", "ok", time.Now(), newestRow(t)-1, "local-fs=active")
	if r := sc(t, "status", "--console"); r.code != 2 || strings.Contains(r.stdout, "The menu shows once more") {
		t.Errorf("after a healthy boot: %+v", r)
	}
}

// In the console form a failure is in the report, which goes to every
// console, not on stderr alone; consoles that do not open are skipped,
// and with none the command's output takes it.
func TestStatusConsoleErrors(t *testing.T) {
	statusEnv(t, "systemd.unit=rescue.target", true)
	devs := t.TempDir()
	active := filepath.Join(t.TempDir(), "active")
	os.WriteFile(active, []byte("tty1 ttyS0\n"), 0o644)
	os.WriteFile(filepath.Join(devs, "tty1"), nil, 0o644) // ttyS0 does not open
	oldActive, oldDev, oldSys := consoleActive, devDir, systemConsole
	consoleActive, devDir = active, devs
	systemConsole = func(io.Writer) bool { return true }
	t.Cleanup(func() { consoleActive, devDir, systemConsole = oldActive, oldDev, oldSys })
	os.Remove(filepath.Join(os.Getenv("SC_HOME"), "changes.db")) // the store is gone
	r := sc(t, "status", "--console")
	tty1, _ := os.ReadFile(filepath.Join(devs, "tty1"))
	if r.code != 1 || r.stderr != "" || r.stdout != "" || !strings.Contains(string(tty1), "\nsc: ") {
		t.Errorf("error: %+v, tty1 %q", r, tty1)
	}
	os.Remove(filepath.Join(devs, "tty1")) // no console opens
	if r := sc(t, "status", "--console"); !strings.Contains(r.stdout, "This boot:") || !strings.Contains(r.stdout, "\nsc: ") {
		t.Errorf("no console: %+v", r)
	}
	// No store in the rescue boot: /var not mounted, not "sc init" (the
	// M4 final review, B4).
	if r := sc(t, "status", "--console"); !strings.Contains(r.stdout, "; if /var is a filesystem of its own: mount /var, then sc status\n") || strings.Contains(r.stdout, "sc init") {
		t.Errorf("no store: %+v", r)
	}
}

// sc status in emergency.service's drop-in: the service is activating and
// its target not yet reached; the mode is still emergency (the step 11
// design found it said normal, and left out daemon-reload and reboot).
func TestStatusModeFromSystemd(t *testing.T) {
	_, fstab, home := statusEnv(t, "BOOT_IMAGE=/vmlinuz ro quiet splash", false)
	good := snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)
	for _, tc := range []struct{ states, mode string }{
		{"activating\\ninactive\\ninactive\\ninactive\\n", "emergency"},
		{"inactive\\ninactive\\nactivating\\ninactive\\n", "rescue"},
		{"inactive\\ninactive\\ninactive\\nactive\\n", "rescue"},
		{"inactive\\ninactive\\ninactive\\ninactive\\n", "normal"},
	} {
		os.WriteFile(filepath.Join(bootRunner.Dirs[0], "systemctl"), []byte("#!/bin/sh\nprintf '"+tc.states+"'\nexit 3\n"), 0o755)
		r := sc(t, "status")
		if !strings.Contains(r.stdout, "("+tc.mode+")") {
			t.Errorf("%q: %+v", tc.states, r)
		}
		if reboot := strings.Contains(r.stdout, "  sc restore "+good+"\n  sync\n  systemctl daemon-reload\n  systemctl reboot\n"); reboot != (tc.mode != "normal") {
			t.Errorf("%q: reboot steps %v:\n%s", tc.states, reboot, r.stdout)
		}
	}
}

// updateGolden rewrites the M4 lab's goldens:
// go test ./cmd/sc -run TestStatusConsoleLab -update
var updateGolden = flag.Bool("update", false, "rewrite lab/testdata/console-*.golden (TestStatusConsoleLab)")

// The M4 lab's boots and its fstab (lab/e2e.py): B1 healthy, the edit,
// B2 broken by it, B3 the rescue boot.
const (
	labB1, labB2, labB3 = "1b1b1b1b-0000-4000-8000-000000000001", "2b2b2b2b-0000-4000-8000-000000000002", "3b3b3b3b-0000-4000-8000-000000000003"
	labGood             = "LABEL=cloudimg-rootfs\t/\t ext4\tdiscard,commit=30,errors=remount-ro\t0 1\n" +
		"LABEL=BOOT\t/boot\text4\tdefaults\t0 2\nLABEL=UEFI\t/boot/efi\tvfat\tumask=0077\t0 1\n"
	labBadLine = "UUID=3f6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c /mnt/backup ext4 defaults 0 2\n"
)

// labRecord records data as /etc/fstab's newest version in the store at
// home, as scd would, without the file itself; it returns the row's id.
func labRecord(t *testing.T, home, data string) string {
	t.Helper()
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Record([]store.Obs{{Path: "/etc/fstab", Origin: store.OriginAuto,
		State: &fsutil.State{Kind: "file", Data: []byte(data), Meta: fsutil.Meta{Mode: 0o644}, Stable: true}}})
	if err != nil || !res[0].Recorded {
		t.Fatalf("record: %v %+v", err, res)
	}
	return res[0].Change.ID
}

// labConsole is sc status --console as the lab's guest prints it from the
// drop-in: in boot 3's rescue.service after boot 2 ended as outcome a (no
// verdict), b (bad, in emergency mode) or c (bad, emergency mode over),
// or, for "emergency", in boot 2's own emergency.service. It returns the
// report and the ids in it.
func labConsole(t *testing.T, outcome string) (out string, ids map[string]string) {
	t.Helper()
	cur, mode, cmdline, ro := labB3, "inactive\\ninactive\\nactivating\\ninactive\\n",
		"BOOT_IMAGE=/boot/vmlinuz-6.8.0-142-generic root=LABEL=cloudimg-rootfs no_timer_check console=tty1 console=ttyS0 ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1", true
	if outcome == "emergency" {
		cur, mode, cmdline, ro = labB2, "activating\\ninactive\\ninactive\\ninactive\\n",
			"BOOT_IMAGE=/vmlinuz-6.8.0-142-generic root=LABEL=cloudimg-rootfs ro no_timer_check console=tty1 console=ttyS0", false
	}
	_, _, home := statusEnv(t, cmdline, ro)
	idFile := filepath.Join(t.TempDir(), "boot_id")
	os.WriteFile(idFile, []byte(cur+"\n"), 0o644)
	boot.IDPath = idFile // statusEnv puts its own back
	// The service is activating (bootMode), as in the drop-in.
	os.WriteFile(filepath.Join(bootRunner.Dirs[0], "systemctl"), []byte("#!/bin/sh\nprintf '"+mode+"'\nexit 3\n"), 0o755)
	g, err := check.ParseGraph("check fstab /etc/fstab\n")
	if err != nil {
		t.Fatal(err)
	}
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Graph = g }

	t0 := time.Now().Add(-time.Hour)
	good := labRecord(t, home, labGood)
	boot.Seen(home, labB1, t0)
	boot.Record(home, labB1, "ok", t0.Add(2*time.Minute), newestRow(t), "local-fs=active emergency=inactive rescue=inactive failed-units=0")
	bad := labRecord(t, home, labGood+labBadLine)
	boot.Seen(home, labB2, t0.Add(20*time.Minute))
	switch outcome {
	case "b":
		boot.Record(home, labB2, "bad", t0.Add(25*time.Minute), newestRow(t), "local-fs=inactive emergency=active rescue=inactive failed-units=1")
	case "c":
		boot.Record(home, labB2, "bad", t0.Add(25*time.Minute), newestRow(t), "local-fs=inactive emergency=inactive rescue=inactive failed-units=1")
	}
	// B3, on a read-only root, writes no line (sc-boot-seen's condition).
	r := sc(t, "status", "--console")
	if r.code != 2 || r.stderr != "" {
		t.Fatalf("%s: %+v", outcome, r)
	}
	return r.stdout, map[string]string{shortBoot(labB1): "<B1:8>", shortBoot(labB2): "<B2:8>", shortBoot(labB3): "<B3:8>", good: "<GOOD>", bad: "<BAD>"}
}

// labNormalise puts the placeholders lab/e2e.py fills in from its run in
// place of what differs between runs: <B1:8> <B2:8> <B3:8> (a boot id's
// first 8 hex digits), <GOOD> <BAD> (snapshot ids), <N> (the bad line's
// number in fstab), <K boots> ("1 boot", "2 boots": the failed boots),
// and the times <YYYY-MM-DD HH:MM>, <MM-DD HH:MM> and <HH:MM>.
func labNormalise(t *testing.T, out string, ids map[string]string) string {
	t.Helper()
	for id, ph := range ids {
		out = regexp.MustCompile(`\b`+regexp.QuoteMeta(id)+`\b`).ReplaceAllString(out, ph)
	}
	for _, r := range []struct {
		re, with string
		need     bool // the line is always there
	}{
		{`(?m)^(Last healthy:  )\d{4}-\d\d-\d\d \d\d:\d\d, `, "${1}<YYYY-MM-DD HH:MM>, ", true},
		{`(?m)^(Failed since:  )1 boot, last \d\d-\d\d \d\d:\d\d: `, "${1}<K boots>, last <MM-DD HH:MM>: ", strings.Contains(out, "\nFailed since:")},
		{`(?m)^<BAD> \d\d:\d\d  (blocker fstab-source-missing, line )4  `, "<BAD> <HH:MM>  ${1}<N>  ", true},
	} {
		re := regexp.MustCompile(r.re)
		if r.need && !re.MatchString(out) {
			t.Errorf("no %s in:\n%s", r.re, out)
		}
		out = re.ReplaceAllString(out, r.with)
	}
	if left := regexp.MustCompile(`\d\d:\d\d|\b[0-9a-f]{6}\b`).FindString(out); left != "" {
		t.Errorf("%q is left in:\n%s", left, out)
	}
	return out
}

// The M4 lab compares what its guest's console shows with these goldens
// (lab/testdata/console-*.golden, checks 2.5 and 3.6), rendered here
// from the code, never copied by hand. Each also says what the lab
// relies on: the reason the boot failed for each outcome of boot 2, the
// undo list exactly, the menu promise, 80 columns and 20 rows.
func TestStatusConsoleLab(t *testing.T) {
	undo := "\nTo put /etc/fstab back:\n" + "%s  sc restore <GOOD>\n  sync\n  systemctl daemon-reload\n  systemctl reboot\n" +
		"%sThe menu shows once more: the first entry, Ubuntu, is the one.\n"
	// Only the emergency shell can end at a login prompt (Ubuntu's getty
	// on the same console); the rescue entry's never did.
	const login = "At a \"login:\" prompt instead of \"#\": log in, then put sudo before each.\n"
	for _, tc := range []struct{ outcome, golden, head, why, remount string }{
		{"a", "console-rescue-a.golden", "This boot:     <B3:8> (rescue), root read-only\n", ": never reached multi-user\n", "  mount -o remount,rw /\n"},
		{"b", "console-rescue-b.golden", "This boot:     <B3:8> (rescue), root read-only\n", ": a mount failed, emergency mode\n", "  mount -o remount,rw /\n"},
		{"c", "console-rescue-c.golden", "This boot:     <B3:8> (rescue), root read-only\n", ": a mount failed\n", "  mount -o remount,rw /\n"},
		{"emergency", "console-emergency.golden", "This boot:     <B2:8> (emergency), root read-write\n", "", ""},
	} {
		out, ids := labConsole(t, tc.outcome)
		for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			if utf8.RuneCountInString(l) > 80 {
				t.Errorf("%s: %d columns: %q", tc.outcome, utf8.RuneCountInString(l), l)
			}
		}
		if n := screenRows(out); n > 20 {
			t.Errorf("%s: %d rows", tc.outcome, n)
		}
		got := labNormalise(t, out, ids)
		for _, want := range []string{tc.head, "Last healthy:  <YYYY-MM-DD HH:MM>, boot <B1:8>\n",
			"\n<BAD> <HH:MM>  blocker fstab-source-missing, line <N>  /etc/fstab\n", fmt.Sprintf(undo, tc.remount, map[bool]string{true: login}[tc.outcome == "emergency"])} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: lacks %q:\n%s", tc.outcome, want, got)
			}
		}
		if tc.outcome != "emergency" && strings.Contains(got, "login:") {
			t.Errorf("%s: the login hint is for the emergency shell only:\n%s", tc.outcome, got)
		}
		if failed := strings.Contains(got, "\nFailed since:  <K boots>, last <MM-DD HH:MM>"+tc.why); failed != (tc.why != "") {
			t.Errorf("%s: Failed since, for %q:\n%s", tc.outcome, tc.why, got)
		}
		path := filepath.Join("../../lab/testdata", tc.golden)
		if *updateGolden {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (go test ./cmd/sc -run TestStatusConsoleLab -update writes it)", err)
		}
		if got != string(want) {
			t.Errorf("%s differs from what sc prints now (-update rewrites it):\n--- golden\n%s--- now\n%s", tc.golden, want, got)
		}
	}
}

// fakeConsole is a console that takes what take says per write and whose
// queue is what queue says; a write it does not finish times out.
type fakeConsole struct {
	got   []byte
	take  func() int
	queue func() int
	err   error
}

func (c *fakeConsole) SetWriteDeadline(time.Time) error { return nil }
func (c *fakeConsole) queued() int                      { return c.queue() }
func (c *fakeConsole) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n := min(c.take(), len(p))
	c.got = append(c.got, p[:n]...)
	if n < len(p) {
		time.Sleep(time.Millisecond)
		return n, os.ErrDeadlineExceeded
	}
	return n, nil
}

// A slow console gets the whole report; only one that moves nothing is
// given up. The M4 lab's serial console took no more of the report for
// 2 s, and the old flat limit cut it in the middle of a line. Each case
// sets the limits it is about and leaves the other generous, so a loaded
// machine (the first version failed under one) cannot decide it.
func TestWriteConsoleSlowOrStopped(t *testing.T) {
	oldStall, oldTurn := consoleStall, consoleTurn
	t.Cleanup(func() { consoleStall, consoleTurn = oldStall, oldTurn })
	limits := func(stall, turn time.Duration) { consoleStall, consoleTurn = stall, turn }
	report := bytes.Repeat([]byte("600940 19:37  blocker fstab-source-missing, line 4  /etc/fstab\n"), 20)
	none := func() int { return -1 }

	// It takes ten bytes at a time: slow, never stopped.
	limits(time.Minute, time.Minute)
	slow := &fakeConsole{take: func() int { return 10 }, queue: none}
	if !writeConsole(slow, report, time.Time{}) || !bytes.Equal(slow.got, report) {
		t.Errorf("slow: got %d of %d bytes", len(slow.got), len(report))
	}

	// A full queue that drains: no byte of the report is taken for twice
	// consoleStall, but the queue shrinks all the while. Then it has room.
	limits(80*time.Millisecond, time.Minute)
	start, q := time.Now(), 4096
	drains := &fakeConsole{
		take: func() int {
			if time.Since(start) < 2*consoleStall {
				return 0
			}
			return 1 << 20
		},
		queue: func() int { q--; return q },
	}
	if !writeConsole(drains, report, time.Time{}) || !bytes.Equal(drains.got, report) {
		t.Errorf("a draining queue: got %d of %d bytes", len(drains.got), len(report))
	}

	// Stopped (XOFF): nothing taken, the queue stands. Given up after
	// consoleStall, long before its turn is over.
	for name, queue := range map[string]func() int{"queue stands": func() int { return 4096 }, "queue not known": none} {
		start = time.Now()
		stopped := &fakeConsole{take: func() int { return 0 }, queue: queue}
		if writeConsole(stopped, report, time.Time{}) || len(stopped.got) != 0 {
			t.Errorf("%s: written", name)
		}
		if d := time.Since(start); d < consoleStall || d > consoleTurn/2 {
			t.Errorf("%s: given up after %s, want about %s", name, d, consoleStall)
		}
	}

	// A byte now and then for ever: its turn ends.
	limits(time.Minute, 300*time.Millisecond)
	start = time.Now()
	trickle := &fakeConsole{take: func() int { time.Sleep(5 * time.Millisecond); return 1 }, queue: none}
	if writeConsole(trickle, bytes.Repeat(report, 100), time.Time{}) || len(trickle.got) == 0 {
		t.Errorf("trickle: written whole, or nothing at all (%d bytes)", len(trickle.got))
	}
	if d := time.Since(start); d < consoleTurn || d > consoleStall/2 {
		t.Errorf("trickle: ended after %s, want about %s", d, consoleTurn)
	}

	// An error that is no timeout ends it at once.
	gone := &fakeConsole{err: syscall.EIO, queue: none}
	if writeConsole(gone, report, time.Time{}) {
		t.Error("EIO: written")
	}
}

// A host pause freezes the whole machine; under TCG sc's own clock jumps
// forward by the length of the pause (the M4 lab records gaps of 10 to
// 380 s). A pause while a console is briefly busy must not be read as a
// console that moved nothing for consoleStall: bios 2.5 of fc47ea3 lost
// the whole rescue report on ttyS0 that way (a 10 s host pause, guest
// journal boot 2 monotonic 107.9->118.2, coincided with sc's writeConsole
// as emergency.service started). One pause counts as one slice, so the
// report still reaches a console that then takes it.
func TestWriteConsoleHostPause(t *testing.T) {
	oldStall, oldTurn := consoleStall, consoleTurn
	t.Cleanup(func() { consoleStall, consoleTurn = oldStall, oldTurn })
	consoleStall, consoleTurn = 2*time.Second, time.Minute
	report := bytes.Repeat([]byte("600940 19:37  blocker fstab-source-missing, line 4  /etc/fstab\n"), 20)
	none := func() int { return -1 }

	// The queue is full (systemd's boot flood), so the first slice takes
	// nothing; then the host pauses for longer than consoleStall, sc's
	// clock jumping with it; then the console drains and takes the report.
	calls := 0
	paused := &fakeConsole{
		take: func() int {
			calls++
			switch calls {
			case 1:
				return 0
			case 2:
				time.Sleep(3 * time.Second) // the host pause: one long slice
				return 0
			default:
				return 1 << 20
			}
		},
		queue: none,
	}
	if !writeConsole(paused, report, time.Time{}) || !bytes.Equal(paused.got, report) {
		t.Errorf("a host pause lost the report: got %d of %d bytes", len(paused.got), len(report))
	}
}

// ttyFile on real devices: a terminal says how much it has queued, and
// asking leaves the file non-blocking (os.File.Fd would not).
func TestTTYFileQueued(t *testing.T) {
	reg, err := os.OpenFile(filepath.Join(t.TempDir(), "tty1"), os.O_WRONLY|os.O_CREATE|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if q := (ttyFile{reg}).queued(); q != -1 {
		t.Errorf("a regular file: queued %d", q)
	}
	if !writeConsole(ttyFile{reg}, []byte("report\n"), time.Time{}) {
		t.Error("a regular file: not written")
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer master.Close()
	var n, unlock int32
	ioctl := func(req uintptr, arg *int32) error {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), req, uintptr(unsafe.Pointer(arg))); e != 0 {
			return e
		}
		return nil
	}
	if err := ioctl(syscall.TIOCSPTLCK, &unlock); err != nil {
		t.Skipf("no pty: %v", err)
	}
	if err := ioctl(syscall.TIOCGPTN, &n); err != nil {
		t.Skipf("no pty: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_WRONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer slave.Close()
	tty := ttyFile{slave}
	if q := tty.queued(); q < 0 {
		t.Errorf("a terminal: queued %d", q)
	}
	if !writeConsole(tty, []byte("This boot:\n"), time.Time{}) {
		t.Fatal("a terminal: not written")
	}
	// Nobody reads the master: the terminal fills up and then stands.
	// Still non-blocking after queued(), so that is a timeout, not a hang.
	oldStall := consoleStall
	consoleStall = 300 * time.Millisecond
	t.Cleanup(func() { consoleStall = oldStall })
	done := make(chan bool, 1)
	go func() { done <- writeConsole(tty, bytes.Repeat([]byte("x"), 1<<20), time.Time{}) }()
	select {
	case ok := <-done:
		if ok {
			t.Error("a megabyte into a terminal nobody reads: written")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("writeConsole hangs on a full terminal")
	}
}

// openPty gives a terminal's two ends and the path of the one sc writes.
func openPty(t *testing.T) (slave *os.File, name string, master *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	var n, unlock int32
	for _, c := range []struct {
		req uintptr
		arg *int32
	}{{syscall.TIOCSPTLCK, &unlock}, {syscall.TIOCGPTN, &n}} {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), c.req, uintptr(unsafe.Pointer(c.arg))); e != 0 {
			t.Skipf("no pty: %v", e)
		}
	}
	name = fmt.Sprintf("/dev/pts/%d", n)
	s, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, name, m
}

// blockingWriter is a console that takes nothing until it is released.
type blockingWriter struct {
	release chan struct{}
	calls   atomic.Int32
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.calls.Add(1)
	<-w.release
	return len(p), nil
}

// The only console is stopped (Scroll Lock, XOFF): it is given up, and
// the report is not written to the command's output instead, which is
// /dev/console, the same stopped device. That write had no limit, and the
// shell never came (the M4 final review, B1).
func TestConsoleStoppedSole(t *testing.T) {
	slave, name, _ := openPty(t)
	const tcxonc, tcooff = 0x540A, 0
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), tcxonc, tcooff); e != 0 {
		t.Skipf("TCXONC: %v", e)
	}
	devs := t.TempDir()
	os.Symlink(name, filepath.Join(devs, "tty1"))
	active := filepath.Join(t.TempDir(), "active")
	os.WriteFile(active, []byte("tty1\n"), 0o644)
	oldActive, oldDev, oldSys, oldStall := consoleActive, devDir, systemConsole, consoleStall
	consoleActive, devDir, systemConsole, consoleStall = active, devs, func(io.Writer) bool { return true }, 300*time.Millisecond
	t.Cleanup(func() { consoleActive, devDir, systemConsole, consoleStall = oldActive, oldDev, oldSys, oldStall })
	out := &blockingWriter{release: make(chan struct{})}
	defer close(out.release)
	done := make(chan struct{})
	start := time.Now()
	go func() { writeConsoles(out, []byte("This boot: x\n"), time.Now().Add(time.Minute)); close(done) }()
	select {
	case <-done:
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("given up after %s", d)
		}
		if out.calls.Load() != 0 {
			t.Error("the report went to the command's output as well")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("writeConsoles hangs on a stopped sole console")
	}
}

// When no console opens, the command's output gets the report, but not
// past the deadline: it may be the same stopped /dev/console. writeConsole
// stops at the deadline too, whatever its turn.
func TestConsoleDeadline(t *testing.T) {
	oldActive, oldSys := consoleActive, systemConsole
	consoleActive, systemConsole = filepath.Join(t.TempDir(), "none"), func(io.Writer) bool { return true }
	t.Cleanup(func() { consoleActive, systemConsole = oldActive, oldSys })
	out := &blockingWriter{release: make(chan struct{})}
	defer close(out.release)
	start := time.Now()
	writeConsoles(out, []byte("This boot: x\n"), time.Now().Add(300*time.Millisecond))
	if d := time.Since(start); d < 250*time.Millisecond || d > 5*time.Second || out.calls.Load() != 1 {
		t.Errorf("returned after %s, %d writes", d, out.calls.Load())
	}
	c := &fakeConsole{take: func() int { return 1 << 20 }, queue: func() int { return 0 }}
	if writeConsole(c, []byte("report\n"), time.Now().Add(-time.Second)) || len(c.got) != 0 {
		t.Errorf("past the deadline: wrote %q", c.got)
	}
}

// The stop line says "60 s", not Go's "1m0s" (the chunk E review).
func TestSeconds(t *testing.T) {
	for d, want := range map[time.Duration]string{consoleLimit: "60 s", 90 * time.Second: "90 s", 300 * time.Millisecond: "300ms"} {
		if got := seconds(d); got != want {
			t.Errorf("seconds(%v) = %q, want %q", d, got, want)
		}
	}
}

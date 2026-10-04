package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/boot"
	"smartconfig/internal/check"
	"smartconfig/internal/store"
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

// The rescue console fits an 80x25 screen: worst first, at most
// consoleRows files, the rest counted; every change is still judged, and
// the undo has the remount, daemon-reload and reboot.
func TestStatusConsole(t *testing.T) {
	dir, fstab, home := statusEnv(t, "root=UUID=x ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1", true)
	good := snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)
	for i := 0; i < 21; i++ {
		snap(t, filepath.Join(dir, fmt.Sprintf("f%02d", i)), "x\n")
	}
	boot.Record(home, "bbbbbbbb-2", "bad", time.Now(), newestRow(t), "local-fs=inactive emergency=active rescue=inactive failed-units=0")
	r := sc(t, "status", "--console")
	if r.code != 2 {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) > 20 {
		t.Errorf("%d lines:\n%s", len(lines), r.stdout)
	}
	for _, l := range lines {
		// Every line but a file's own path fits 80 columns.
		if len(strings.ReplaceAll(l, dir, "/etc")) > 80 {
			t.Errorf("wider than 80 columns: %q", l)
		}
	}
	for _, want := range []string{
		"This boot:     cccccccc (rescue), root read-only\n",
		"Failed since:  1 boot, last ", ": a mount failed, emergency mode\n",
		"\nChanged since the last healthy boot, worst first:\n",
		"blocker fstab-source-missing, line 1  " + fstab + "\n",
		"16 files more (sc status lists them all)\n",
		"\nTo put " + fstab + " back as it was during the last healthy boot:\n  mount -o remount,rw /\n  sc restore " + good + "\n  sync\n  systemctl daemon-reload\n  systemctl reboot\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("lacks %q:\n%s", want, r.stdout)
		}
	}
	// Worst first: the blocker is the first file listed.
	if !strings.Contains(lines[6], fstab) {
		t.Errorf("not worst first:\n%s", r.stdout)
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
	snap(t, fstab, badLine) // new since
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

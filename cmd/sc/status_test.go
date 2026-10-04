package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/boot"
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
	oldID, oldCL, oldRO, oldProc := boot.IDPath, cmdlinePath, rootReadOnly, procDir
	boot.IDPath, cmdlinePath, procDir = p, cl, t.TempDir()
	rootReadOnly = func() bool { return ro }
	t.Cleanup(func() { boot.IDPath, cmdlinePath, rootReadOnly, procDir = oldID, oldCL, oldRO, oldProc })
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
		"Failed since:  1 boot, the last at ",
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

// The rescue console: a read-only root gets the remount and the reboot,
// and at most 20 files are listed.
func TestStatusConsole(t *testing.T) {
	dir, fstab, home := statusEnv(t, "root=UUID=x ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1", true)
	good := snap(t, fstab, goodLine)
	boot.Record(home, "aaaaaaaa-1", "ok", time.Now(), newestRow(t), "local-fs=active")
	snap(t, fstab, badLine)
	for i := 0; i < 21; i++ {
		snap(t, filepath.Join(dir, fmt.Sprintf("f%02d", i)), "x\n")
	}
	r := sc(t, "status", "--console")
	if r.code != 2 {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		"This boot:     cccccccc (rescue), root read-only\n",
		"2 files more (sc status lists them all)\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("lacks %q:\n%s", want, r.stdout)
		}
	}
	// The fstab change is the 22nd newest, past the 20 lines, and still
	// judged: exit 2 above, and its undo, rescue form.
	if strings.Contains(r.stdout, fstab+"  ") || !strings.Contains(r.stdout, "  mount -o remount,rw /\n  sc restore "+good+"\n  sync\n  systemctl reboot\n") {
		t.Errorf("rescue undo:\n%s", r.stdout)
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

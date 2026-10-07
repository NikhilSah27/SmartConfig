package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMounts points fstabPath and mountinfoPath at files with this
// content for the test.
func fakeMounts(t *testing.T, fstab, mountinfo string) {
	t.Helper()
	dir := t.TempDir()
	oldF, oldM := fstabPath, mountinfoPath
	fstabPath, mountinfoPath = filepath.Join(dir, "fstab"), filepath.Join(dir, "mountinfo")
	t.Cleanup(func() { fstabPath, mountinfoPath = oldF, oldM })
	os.WriteFile(fstabPath, []byte(fstab), 0o644)
	os.WriteFile(mountinfoPath, []byte(mountinfo), 0o644)
}

const rootMount = "22 1 8:2 / / rw,relatime - ext4 /dev/sda2 rw\n"

// A restore under an fstab mount point that is not mounted (a separate
// /boot in the rescue shell) writes nothing and says to mount it: the
// write would land on the root filesystem's copy, which nothing reads, and
// say it worked (M4 follow-up 1). Mounted, it goes ahead.
func TestRestoreUnmountedMount(t *testing.T) {
	t.Setenv("SC_HOME", t.TempDir())
	mustSC(t, "init")
	boot := t.TempDir()
	p := filepath.Join(boot, "grub", "grub.cfg")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("good\n"), 0o644)
	id := strings.TrimSpace(mustSC(t, "snapshot", "-q", p))
	os.WriteFile(p, []byte("bad\n"), 0o644)
	fstab := "UUID=a / ext4 defaults 0 1\nUUID=b " + boot + " ext4 defaults 0 2\n/swapfile none swap sw 0 0\n"
	fakeMounts(t, fstab, rootMount)
	r := sc(t, "restore", id)
	if r.code != 1 || r.stderr != "sc: "+p+" is under "+boot+", which /etc/fstab mounts and is not mounted: run mount "+boot+", then this again (file not changed)\n" {
		t.Errorf("not mounted: %+v", r)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Errorf("written while not mounted: %q", b)
	}
	fakeMounts(t, fstab, rootMount+"40 22 8:1 / "+boot+" rw,relatime - ext4 /dev/sda1 rw\n")
	mustSC(t, "restore", id)
	if b, _ := os.ReadFile(p); string(b) != "good\n" {
		t.Errorf("mounted, not restored: %q", b)
	}
}

// The deepest mount point counts; an escaped name in fstab and in the
// mount table; and with no mount table to read, sc cannot tell and does
// not refuse.
func TestUnmountedMount(t *testing.T) {
	fstab := "UUID=a / ext4 defaults 0 1\nUUID=b /srv ext4 defaults 0 2\nUUID=c /srv/data\\040x ext4 defaults 0 2\n" +
		"UUID=d none swap sw 0 0\n# UUID=e /srv/old ext4 defaults 0 2\n"
	fakeMounts(t, fstab, rootMount+"30 22 8:3 / /srv rw - ext4 /dev/sda3 rw\n")
	for path, want := range map[string]string{
		"/srv/data x/f":  "/srv/data x",
		"/srv/data x":    "/srv/data x",
		"/srv/other/f":   "",
		"/srv/old/f":     "",
		"/etc/fstab":     "",
		"/srv/data xy/f": "",
	} {
		if got := unmountedMount(path); got != want {
			t.Errorf("unmountedMount(%q) = %q, want %q", path, got, want)
		}
	}
	fakeMounts(t, fstab, rootMount+"30 22 8:3 / /srv rw - ext4 /dev/sda3 rw\n31 30 8:4 / /srv/data\\040x rw - ext4 /dev/sda4 rw\n")
	if got := unmountedMount("/srv/data x/f"); got != "" {
		t.Errorf("mounted with an escape: %q", got)
	}
	mountinfoPath = filepath.Join(t.TempDir(), "none")
	if got := unmountedMount("/srv/data x/f"); got != "" {
		t.Errorf("no mount table: %q", got)
	}
}

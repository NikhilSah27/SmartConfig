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
// say it worked (M4 follow-up 1). Mounted, it goes ahead. A name with a
// space is quoted for the shell.
func TestRestoreUnmountedMount(t *testing.T) {
	t.Setenv("SC_HOME", t.TempDir())
	mustSC(t, "init")
	boot := filepath.Join(t.TempDir(), "my boot")
	p := filepath.Join(boot, "grub", "grub.cfg")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("good\n"), 0o644)
	id := strings.TrimSpace(mustSC(t, "snapshot", "-q", p))
	os.WriteFile(p, []byte("bad\n"), 0o644)
	esc := strings.ReplaceAll(boot, " ", `\040`)
	fstab := "UUID=a / ext4 defaults 0 1\nUUID=b " + esc + " ext4 defaults 0 2\n/swapfile none swap sw 0 0\n"
	fakeMounts(t, fstab, rootMount)
	r := sc(t, "restore", id)
	if r.code != 1 || r.stderr != "sc: "+p+" is under "+boot+", which /etc/fstab mounts and is not mounted: run mount '"+boot+"', then this again (file not changed)\n" {
		t.Errorf("not mounted: %+v", r)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Errorf("written while not mounted: %q", b)
	}
	fakeMounts(t, fstab, rootMount+"40 22 8:1 / "+esc+" rw,relatime - ext4 /dev/sda1 rw\n")
	mustSC(t, "restore", id)
	if b, _ := os.ReadFile(p); string(b) != "good\n" {
		t.Errorf("mounted, not restored: %q", b)
	}
}

// Every mount point the path is under from the deepest one a path
// reaches down, the outermost first; an escaped name in fstab and in the
// mount table; and with no mount table to read, sc cannot tell and does
// not refuse.
func TestUnmountedMounts(t *testing.T) {
	fstab := "UUID=a / ext4 defaults 0 1\nUUID=b /srv ext4 defaults 0 2\nUUID=c /srv/data\\040x/ ext4 defaults 0 2\n" +
		"UUID=d none swap sw 0 0\n# UUID=e /srv/old ext4 defaults 0 2\n"
	check := func(what, path string, want ...string) {
		t.Helper()
		if got := unmountedMounts(path); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: unmountedMounts(%q) = %q, want %q", what, path, got, want)
		}
	}
	fakeMounts(t, fstab, rootMount+"30 22 8:3 / /srv rw - ext4 /dev/sda3 rw\n")
	check("/srv mounted", "/srv/data x/f", "/srv/data x")
	check("/srv mounted", "/srv/other/f")
	check("/srv mounted", "/srv/old/f")
	check("/srv mounted", "/etc/fstab")
	check("/srv mounted", "/srv/data xy/f")
	// Neither mounted: /srv first, or mount /srv/data x has no
	// directory to go on (the chunk G review).
	fakeMounts(t, fstab, rootMount)
	check("none mounted", "/srv/data x/f", "/srv", "/srv/data x")
	// Both mounted, the inner one on the outer one.
	fakeMounts(t, fstab, rootMount+"30 22 8:3 / /srv rw - ext4 /dev/sda3 rw\n31 30 8:4 / /srv/data\\040x rw - ext4 /dev/sda4 rw\n")
	check("both mounted", "/srv/data x/f")
	// The inner one mounted first, then the outer one on top of it: the
	// table still lists it, but a path no longer reaches it.
	fakeMounts(t, fstab, rootMount+"31 22 8:4 / /srv/data\\040x rw - ext4 /dev/sda4 rw\n30 22 8:3 / /srv rw - ext4 /dev/sda3 rw\n")
	check("covered", "/srv/data x/f", "/srv/data x")
	// The inner one alone, on the root's directory: the write reaches it.
	fakeMounts(t, fstab, rootMount+"31 22 8:4 / /srv/data\\040x rw - ext4 /dev/sda4 rw\n")
	check("inner alone", "/srv/data x/f")
	// Stacked: a second mount on /srv over the first.
	fakeMounts(t, fstab, rootMount+"30 22 8:3 / /srv rw - ext4 /dev/sda3 rw\n31 30 8:4 / /srv/data\\040x rw - ext4 /dev/sda4 rw\n"+
		"32 30 8:5 / /srv rw - ext4 /dev/sda5 rw\n")
	check("stacked", "/srv/data x/f", "/srv/data x")
	mountinfoPath = filepath.Join(t.TempDir(), "none")
	check("no mount table", "/srv/data x/f")
}

// A mount point in fstab that is a symlink is mounted where it leads
// (the mount table names that), and fstab itself is never under one of
// its own lines: a bad line must not block its undo (the chunk G review).
func TestUnmountedMountsLinkAndSelf(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "real")
	os.Mkdir(real, 0o755)
	os.Symlink(real, filepath.Join(d, "link"))
	fstab := "UUID=a / ext4 defaults 0 1\nUUID=b " + filepath.Join(d, "link") + " ext4 defaults 0 2\nUUID=typo " + d + " ext4 defaults 0 2\n"
	fakeMounts(t, fstab, rootMount)
	if got := unmountedMounts(filepath.Join(real, "f")); strings.Join(got, "|") != d+"|"+real {
		t.Errorf("through a link, not mounted: %q", got)
	}
	fakeMounts(t, fstab, rootMount+"40 22 8:1 / "+d+" rw - ext4 /dev/sda1 rw\n41 40 8:3 / "+real+" rw - ext4 /dev/sda3 rw\n")
	if got := unmountedMounts(filepath.Join(real, "f")); got != nil {
		t.Errorf("through a link, mounted: %q", got)
	}
	fakeMounts(t, "", rootMount)
	os.WriteFile(fstabPath, []byte("UUID=typo "+filepath.Dir(fstabPath)+" ext4 defaults 0 2\n"), 0o644)
	if got := unmountedMounts(fstabPath); got != nil {
		t.Errorf("fstab under its own line: %q", got)
	}
}

// A file that is itself a mount point in fstab (a bind mount) is not
// put back in place, mounted or not: the write would go to the file
// below, or fail on the mount (the chunk G review).
func TestRestoreFileMountPoint(t *testing.T) {
	t.Setenv("SC_HOME", t.TempDir())
	mustSC(t, "init")
	p := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(p, []byte("good\n"), 0o644)
	id := strings.TrimSpace(mustSC(t, "snapshot", "-q", p))
	os.WriteFile(p, []byte("bad\n"), 0o644)
	fakeMounts(t, "/srv/hosts "+p+" none bind 0 0\n", rootMount)
	r := sc(t, "restore", id)
	if r.code != 1 || r.stderr != "sc: "+p+" is itself a mount point in /etc/fstab: put back the file mounted on it instead (file not changed)\n" {
		t.Errorf("%+v", r)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Errorf("written: %q", b)
	}
}

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildDeb runs scripts/build-deb.sh with a made-up sc and the git inputs
// fixed; the .deb's path.
func buildDeb(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("no dpkg-deb")
	}
	dir := t.TempDir()
	sc := filepath.Join(dir, "sc")
	os.WriteFile(sc, []byte("#!/bin/sh\necho sc\n"), 0o755)
	out := filepath.Join(dir, name)
	cmd := exec.Command("sh", "scripts/build-deb.sh", out)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "SC_BIN="+sc, "VERSION=0.5.0", "DEB_MAINTAINER=Test Owner <owner@example.org>",
		"SOURCE_DATE_EPOCH=1790000000", "REVISION=0123456789ab")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build-deb.sh: %v\n%s", err, b)
	}
	return out
}

// The package (M5 plan 2): the files where a package puts them, sc in
// /usr/sbin, the units and drop-ins in /usr/lib/systemd/system, the GRUB
// script a conffile in /etc/grub.d, root's, with md5sums that match; the
// same inputs build the same bytes.
func TestBuildDeb(t *testing.T) {
	deb := buildDeb(t, "a.deb")
	run := func(args ...string) string {
		t.Helper()
		b, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, b)
		}
		return string(b)
	}
	control := run("dpkg-deb", "--field", deb)
	for _, want := range []string{"Package: smartconfig\n", "Version: 0.5.0\n", "Architecture: amd64\n",
		"Maintainer: Test Owner <owner@example.org>\n", "Depends: systemd (>= 255), grub2-common\n", "Section: admin\n"} {
		if !strings.Contains(control, want) {
			t.Errorf("control lacks %q:\n%s", want, control)
		}
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(run("dpkg-deb", "--contents", deb)), "\n") {
		f := strings.Fields(l)
		if len(f) == 6 && !strings.HasSuffix(f[5], "/") {
			files = append(files, f[0]+" "+f[1]+" "+f[5])
		}
	}
	want := []string{
		"-rwxr-xr-x root/root ./etc/grub.d/42_smartconfig",
		"-rw-r--r-- root/root ./usr/lib/systemd/system/emergency.service.d/50-smartconfig.conf",
		"-rw-r--r-- root/root ./usr/lib/systemd/system/rescue.service.d/50-smartconfig.conf",
		"-rw-r--r-- root/root ./usr/lib/systemd/system/sc-boot-ok.service",
		"-rw-r--r-- root/root ./usr/lib/systemd/system/sc-boot-seen.service",
		"-rw-r--r-- root/root ./usr/lib/systemd/system/scd.service",
		"-rwxr-xr-x root/root ./usr/sbin/sc",
		"-rw-r--r-- root/root ./usr/share/doc/smartconfig/README.md",
		"-rw-r--r-- root/root ./usr/share/doc/smartconfig/changelog.Debian.gz",
		"-rw-r--r-- root/root ./usr/share/doc/smartconfig/copyright",
	}
	if strings.Join(files, "\n") != strings.Join(want, "\n") {
		t.Errorf("contents:\n%s\nnot\n%s", strings.Join(files, "\n"), strings.Join(want, "\n"))
	}
	x := t.TempDir()
	run("dpkg-deb", "--extract", deb, x)
	run("dpkg-deb", "--control", deb, filepath.Join(x, "DEBIAN"))
	if c, _ := os.ReadFile(filepath.Join(x, "DEBIAN", "conffiles")); string(c) != "/etc/grub.d/42_smartconfig\n" {
		t.Errorf("conffiles %q", c)
	}
	sums := exec.Command("md5sum", "--check", "--quiet", "DEBIAN/md5sums")
	sums.Dir = x
	if b, err := sums.CombinedOutput(); err != nil {
		t.Errorf("md5sums: %v\n%s", err, b)
	}
	if b, _ := os.ReadFile(filepath.Join(x, "usr/lib/systemd/system/scd.service")); !strings.Contains(string(b), "\nExecStart=/usr/sbin/sc watch\n") {
		t.Errorf("scd.service:\n%s", b)
	}
	sha := func(p string) string {
		b, _ := os.ReadFile(p)
		return fmt.Sprintf("%x", sha256.Sum256(b))
	}
	if again := buildDeb(t, "b.deb"); sha(again) != sha(deb) {
		t.Errorf("two builds of the same inputs differ")
	}
}

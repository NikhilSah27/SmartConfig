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
	for _, f := range []string{"preinst", "postinst", "prerm", "postrm"} {
		if fi, err := os.Stat(filepath.Join(x, "DEBIAN", f)); err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("DEBIAN/%s: %v %v", f, fi, err)
		}
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

// The maintainer scripts (M5 plan 3) against a made-up root (DPKG_ROOT)
// and tools that only log: what each does on an install, an upgrade, a
// remove and a purge.
func TestDebScripts(t *testing.T) {
	type result struct {
		code     int
		log, err string
	}
	run := func(t *testing.T, script string, env []string, args ...string) result {
		t.Helper()
		root, bin := t.TempDir(), t.TempDir()
		for _, d := range []string{"run/systemd/system", "boot/grub", "var/lib/smartconfig"} {
			os.MkdirAll(filepath.Join(root, d), 0o755)
		}
		os.WriteFile(filepath.Join(root, "boot/grub/grub.cfg"), nil, 0o644)
		os.WriteFile(filepath.Join(root, "boot/grub/grubenv"), nil, 0o644)
		log := filepath.Join(bin, "log")
		for _, tool := range []string{"deb-systemd-helper", "deb-systemd-invoke", "systemctl", "update-grub", "grub-editenv"} {
			body := "#!/bin/sh\necho \"" + tool + " $*\" >>" + log + "\n"
			switch tool {
			case "deb-systemd-helper":
				body += `[ "$1 $2" != "--quiet was-enabled" ] || [ -z "$SC_NOT_ENABLED" ]` + "\n"
			case "update-grub":
				body += `[ -z "$SC_GRUB_FAILS" ]` + "\n"
			}
			os.WriteFile(filepath.Join(bin, tool), []byte(body), 0o755)
		}
		cmd := exec.Command("sh", append([]string{"../../scripts/deb/" + script}, args...)...)
		cmd.Env = append(os.Environ(), append(env, "PATH="+bin+":/usr/bin:/bin", "DPKG_ROOT="+root)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(log)
		return result{code, strings.ReplaceAll(string(b), root, "ROOT"), stderr.String()}
	}
	enable := ""
	for _, u := range []string{"sc-boot-seen.service", "sc-boot-ok.service", "scd.service"} {
		enable += "deb-systemd-helper unmask " + u + "\ndeb-systemd-helper --quiet was-enabled " + u + "\ndeb-systemd-helper enable " + u + "\n"
	}
	for _, tc := range []struct {
		name, script string
		env, args    []string
		log, err     string
	}{
		{"install", "postinst", nil, []string{"configure", ""},
			enable + "systemctl --system daemon-reload\ndeb-systemd-invoke restart scd.service\nupdate-grub \n", ""},
		{"upgrade", "postinst", nil, []string{"configure", "0.4.99+git20261008023503.643cec3"},
			enable + "systemctl --system daemon-reload\ndeb-systemd-invoke restart scd.service\nupdate-grub \n", ""},
		{"disabled by the owner", "postinst", []string{"SC_NOT_ENABLED=1"}, []string{"configure", "0.5.0"},
			strings.ReplaceAll(enable, "helper enable", "helper update-state") +
				"systemctl --system daemon-reload\ndeb-systemd-invoke restart scd.service\nupdate-grub \n", ""},
		{"update-grub fails", "postinst", []string{"SC_GRUB_FAILS=1"}, []string{"configure", ""},
			enable + "systemctl --system daemon-reload\ndeb-systemd-invoke restart scd.service\nupdate-grub \n",
			"smartconfig: update-grub failed; grub.cfg has no rescue entry yet: run sudo update-grub\n"},
		{"remove: stop", "prerm", nil, []string{"remove"}, "deb-systemd-invoke stop scd.service\n", ""},
		{"upgrade: no stop", "prerm", nil, []string{"upgrade", "0.5.1"}, "", ""},
		{"remove", "postrm", nil, []string{"remove"},
			"deb-systemd-helper mask sc-boot-seen.service\ndeb-systemd-helper mask sc-boot-ok.service\ndeb-systemd-helper mask scd.service\n" +
				"systemctl --system daemon-reload\nupdate-grub \ngrub-editenv ROOT/boot/grub/grubenv unset smartconfig_pending\n", ""},
		{"purge", "postrm", nil, []string{"purge"},
			"deb-systemd-helper purge sc-boot-seen.service\ndeb-systemd-helper unmask sc-boot-seen.service\n" +
				"deb-systemd-helper purge sc-boot-ok.service\ndeb-systemd-helper unmask sc-boot-ok.service\n" +
				"deb-systemd-helper purge scd.service\ndeb-systemd-helper unmask scd.service\n",
			"smartconfig: the change history in /var/lib/smartconfig is kept; to delete it: sudo rm -r /var/lib/smartconfig\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, tc.script, tc.env, tc.args...)
			if r.code != 0 || r.log != tc.log || r.err != tc.err {
				t.Errorf("exit %d\nlog:\n%s\nwant:\n%s\nstderr %q, want %q", r.code, r.log, tc.log, r.err, tc.err)
			}
		})
	}
}

// A hand install (the README's before the package) is taken over on the
// first install: what preinst recognises as SmartConfig's is disabled and
// moved aside as NAME.dpkg-old, not executable; anything else is left and
// named; an upgrade of the package moves nothing (M5 plan, question 4).
func TestDebPreinstHandInstall(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	files := map[string]string{
		"etc/systemd/system/scd.service":                             "[Unit]\nDocumentation=https://github.com/NikhilSah27/SmartConfig\n",
		"etc/systemd/system/sc-boot-seen.service":                    "[Unit]\nDocumentation=https://github.com/NikhilSah27/SmartConfig\n",
		"etc/systemd/system/sc-boot-ok.service":                      "[Unit]\nDescription=someone else's\n",
		"etc/systemd/system/rescue.service.d/50-smartconfig.conf":    "[Service]\nExecStartPre=-/usr/local/sbin/sc status --console\n",
		"etc/systemd/system/emergency.service.d/50-smartconfig.conf": "[Service]\nExecStartPre=-/usr/local/sbin/sc status --console\n",
		"etc/grub.d/42_smartconfig":                                  "#!/bin/sh\n# 42_smartconfig: SmartConfig's part of grub.cfg (M4 plan 3.3, 3.4).\n",
		"usr/local/sbin/sc":                                          "ELF smartconfig/internal/store go1.26.8",
	}
	for p, data := range files {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(data), 0o755)
	}
	log := filepath.Join(bin, "log")
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho \"systemctl $*\" >>"+log+"\n"), 0o755)
	run := func(args ...string) string {
		cmd := exec.Command("sh", append([]string{"../../scripts/deb/preinst"}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "DPKG_ROOT="+root)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("preinst %v: %v\n%s", args, err, stderr.String())
		}
		return stderr.String()
	}
	if msg := run("upgrade", "0.5.0"); msg != "" {
		t.Errorf("an upgrade said %q", msg)
	}
	msg := run("install")
	for _, p := range []string{"etc/systemd/system/scd.service", "etc/systemd/system/sc-boot-seen.service",
		"etc/systemd/system/rescue.service.d/50-smartconfig.conf", "etc/systemd/system/emergency.service.d/50-smartconfig.conf",
		"etc/grub.d/42_smartconfig", "usr/local/sbin/sc"} {
		fi, err := os.Stat(filepath.Join(root, p+".dpkg-old"))
		if _, gone := os.Stat(filepath.Join(root, p)); err != nil || fi.Mode().Perm()&0o111 != 0 || gone == nil {
			t.Errorf("%s not moved aside: %v %v", p, fi, err)
		}
		if !strings.Contains(msg, "the hand-installed /"+p+" is now /"+p+".dpkg-old\n") {
			t.Errorf("not said: %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/sc-boot-ok.service")); err != nil ||
		!strings.Contains(msg, "/etc/systemd/system/sc-boot-ok.service is not SmartConfig's: left") {
		t.Errorf("someone else's unit: %v\n%s", err, msg)
	}
	if b, _ := os.ReadFile(log); string(b) != "systemctl --root="+root+" disable scd.service\nsystemctl --root="+root+" disable sc-boot-seen.service\n" {
		t.Errorf("systemctl:\n%s", b)
	}
}

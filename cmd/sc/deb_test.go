package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// buildDeb runs scripts/build-deb.sh with a made-up sc and the git inputs
// fixed; the .deb's path.
func buildDeb(t *testing.T, name string, env ...string) string {
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
	cmd.Env = append(cmd.Env, env...)
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
		"-rw-r--r-- root/root ./usr/share/doc/smartconfig/changelog.gz",
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
	// md5sums: every file but the conffile (dh_md5sums); Installed-Size:
	// each file's KiB rounded up and 1 for each directory (dpkg-gencontrol).
	var listed, inTree []string
	b, _ := os.ReadFile(filepath.Join(x, "DEBIAN/md5sums"))
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		listed = append(listed, strings.Fields(l)[1])
	}
	size := int64(0)
	filepath.WalkDir(x, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(x, p)
		switch {
		case rel == ".":
		case rel == "DEBIAN":
			return filepath.SkipDir
		case d.Type().IsRegular():
			fi, _ := d.Info()
			size += (fi.Size() + 1023) / 1024
			if rel != "etc/grub.d/42_smartconfig" {
				inTree = append(inTree, rel)
			}
		default:
			size++
		}
		return nil
	})
	slices.Sort(inTree)
	if !slices.Equal(listed, inTree) {
		t.Errorf("md5sums lists %v, not %v", listed, inTree)
	}
	if want := fmt.Sprintf("Installed-Size: %d\n", size); !strings.Contains(control, want) {
		t.Errorf("control lacks %q:\n%s", want, control)
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
	// Staged on another file system (tmpfs: directories of no blocks), the
	// same bytes (the M5 review, A4).
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		if shm := buildDeb(t, "c.deb", "TMPDIR=/dev/shm"); sha(shm) != sha(deb) {
			t.Errorf("staged in /dev/shm, the build differs")
		}
	}
}

// debScript runs scripts/deb/SCRIPT with DPKG_ROOT=root and, first in
// PATH, tools that only log to bin/log (deb-systemd-helper's was-enabled
// and systemctl is-enabled fail with SC_NOT_ENABLED set, update-grub with
// SC_GRUB_FAILS; systemctl is-active scd succeeds with SC_ACTIVE set, and
// its show MainPID gives SC_PIDS' first and second word in turn, by
// default 611 then 742: a restart); its exit code and stderr, and the log
// so far with root written ROOT.
func debScript(t *testing.T, root, bin, script string, env []string, args ...string) (int, string, string) {
	t.Helper()
	log := filepath.Join(bin, "log")
	for _, tool := range []string{"deb-systemd-helper", "deb-systemd-invoke", "systemctl", "update-grub", "grub-editenv"} {
		body := "#!/bin/sh\necho \"" + tool + " $*\" >>" + log + "\n"
		switch tool {
		case "deb-systemd-helper":
			body += `[ "$1 $2" != "--quiet was-enabled" ] || [ -z "$SC_NOT_ENABLED" ]` + "\n"
		case "update-grub":
			body += `[ -z "$SC_GRUB_FAILS" ]` + "\n"
		case "systemctl":
			body += `case "$*" in
"show -p MainPID --value scd.service")
	i=$(cat "` + bin + `/calls" 2>/dev/null || echo 0); echo $((i + 1)) >"` + bin + `/calls"
	set -- ${SC_PIDS:-611 742}
	if [ $((i % 2)) = 0 ]; then echo "$1"; else echo "$2"; fi ;;
"--quiet is-enabled scd.service") [ -z "$SC_NOT_ENABLED" ] ;;
"--quiet is-active scd.service") [ -n "$SC_ACTIVE" ] ;;
esac
`
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
	return code, strings.ReplaceAll(string(b), root, "ROOT"), stderr.String()
}

// The maintainer scripts (M5 plan 3) against a made-up root (DPKG_ROOT)
// and tools that only log: what each does on an install, an upgrade, a
// remove and a purge, with and without systemd running, GRUB or a store.
func TestDebScripts(t *testing.T) {
	enable := ""
	for _, u := range []string{"sc-boot-seen.service", "sc-boot-ok.service", "scd.service"} {
		enable += "deb-systemd-helper unmask " + u + "\ndeb-systemd-helper --quiet was-enabled " + u + "\ndeb-systemd-helper enable " + u + "\n"
	}
	show := "systemctl show -p MainPID --value scd.service\n"
	restart := "systemctl --system daemon-reload\n" + show + "deb-systemd-invoke restart scd.service\n" + show
	notRestarted := "smartconfig: scd was not restarted (does /usr/sbin/policy-rc.d forbid it?): sudo systemctl restart scd\n"
	isEnabled := "systemctl --quiet is-enabled scd.service\n"
	isActive := "systemctl --quiet is-active scd.service\n"
	mask := "deb-systemd-helper mask sc-boot-seen.service\ndeb-systemd-helper mask sc-boot-ok.service\ndeb-systemd-helper mask scd.service\n"
	purge := "deb-systemd-helper purge sc-boot-seen.service\ndeb-systemd-helper unmask sc-boot-seen.service\n" +
		"deb-systemd-helper purge sc-boot-ok.service\ndeb-systemd-helper unmask sc-boot-ok.service\n" +
		"deb-systemd-helper purge scd.service\ndeb-systemd-helper unmask scd.service\n"
	kept := "smartconfig: the change history in /var/lib/smartconfig is kept; to delete it: sudo rm -r /var/lib/smartconfig\n"
	for _, tc := range []struct {
		name, script string
		without      []string // of run/systemd/system, boot/grub, var/lib/smartconfig
		env, args    []string
		log, err     string
	}{
		{"install", "postinst", nil, nil, []string{"configure", ""}, enable + restart + "update-grub \n", ""},
		{"upgrade", "postinst", nil, nil, []string{"configure", "0.4.99+git20261008023503.643cec3"}, enable + restart + "update-grub \n", ""},
		{"abort-upgrade", "postinst", nil, nil, []string{"abort-upgrade", "0.5.1"}, enable + restart + "update-grub \n", ""},
		{"disabled by the owner", "postinst", nil, []string{"SC_NOT_ENABLED=1"}, []string{"configure", "0.5.0"},
			strings.ReplaceAll(enable, "helper enable", "helper update-state") + restart + "update-grub \n", ""},
		{"update-grub fails", "postinst", nil, []string{"SC_GRUB_FAILS=1"}, []string{"configure", ""},
			enable + restart + "update-grub \n", "smartconfig: update-grub failed; grub.cfg has no rescue entry yet: run sudo update-grub\n"},
		{"install, no systemd running", "postinst", []string{"run/systemd/system"}, nil, []string{"configure", ""}, enable + "update-grub \n", ""},
		// /usr/sbin/policy-rc.d forbids the restart (this VM's image, M5's
		// sign-off): scd runs on as it was, or not at all.
		{"the restart forbidden", "postinst", nil, []string{"SC_PIDS=611 611"}, []string{"configure", ""},
			enable + restart + "update-grub \n", notRestarted},
		{"the start forbidden", "postinst", nil, []string{"SC_PIDS=0 0"}, []string{"configure", ""},
			enable + restart + isEnabled + "update-grub \n", notRestarted},
		{"not running, the owner disabled it", "postinst", nil, []string{"SC_PIDS=0 0", "SC_NOT_ENABLED=1"}, []string{"configure", "0.5.0"},
			strings.ReplaceAll(enable, "helper enable", "helper update-state") + restart + isEnabled + "update-grub \n", ""},
		{"install, no GRUB", "postinst", []string{"boot/grub"}, nil, []string{"configure", ""}, enable + restart, ""},
		{"remove: stop", "prerm", nil, nil, []string{"remove"}, "deb-systemd-invoke stop scd.service\n" + isActive, ""},
		{"remove: the stop forbidden", "prerm", nil, []string{"SC_ACTIVE=1"}, []string{"remove"},
			"deb-systemd-invoke stop scd.service\n" + isActive,
			"smartconfig: scd still runs (does /usr/sbin/policy-rc.d forbid stopping it?): sudo systemctl stop scd\n"},
		{"remove: no systemd running", "prerm", []string{"run/systemd/system"}, nil, []string{"remove"}, "", ""},
		{"upgrade: no stop", "prerm", nil, nil, []string{"upgrade", "0.5.1"}, "", ""},
		{"remove", "postrm", nil, nil, []string{"remove"},
			mask + "systemctl --system daemon-reload\nupdate-grub \ngrub-editenv ROOT/boot/grub/grubenv unset smartconfig_pending\n", ""},
		{"remove, no systemd running", "postrm", []string{"run/systemd/system"}, nil, []string{"remove"},
			mask + "update-grub \ngrub-editenv ROOT/boot/grub/grubenv unset smartconfig_pending\n", ""},
		{"remove, no GRUB", "postrm", []string{"boot/grub"}, nil, []string{"remove"}, mask + "systemctl --system daemon-reload\n", ""},
		{"purge", "postrm", nil, nil, []string{"purge"}, purge, kept},
		{"purge, no store", "postrm", []string{"var/lib/smartconfig"}, nil, []string{"purge"}, purge, ""},
		{"abort-install, nothing moved", "postrm", nil, nil, []string{"abort-install"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := t.TempDir(), t.TempDir()
			for _, d := range []string{"run/systemd/system", "boot/grub", "var/lib/smartconfig"} {
				if !slices.Contains(tc.without, d) {
					os.MkdirAll(filepath.Join(root, d), 0o755)
				}
			}
			if !slices.Contains(tc.without, "boot/grub") {
				os.WriteFile(filepath.Join(root, "boot/grub/grub.cfg"), nil, 0o644)
				os.WriteFile(filepath.Join(root, "boot/grub/grubenv"), nil, 0o644)
			}
			code, log, stderr := debScript(t, root, bin, tc.script, tc.env, tc.args...)
			if code != 0 || log != tc.log || stderr != tc.err {
				t.Errorf("exit %d\nlog:\n%s\nwant:\n%s\nstderr %q, want %q", code, log, tc.log, stderr, tc.err)
			}
		})
	}
}

// A hand install (the README's before the package) is taken over on the
// first install only (M5 plan, question 4): preinst moves its
// 42_smartconfig aside before the unpack (postrm abort-install puts it
// back), postinst's first configure the rest. What is recognised as
// SmartConfig's is disabled and moved aside as NAME.dpkg-old, not
// executable; anything else is left, and named. After a remove, dpkg
// passes the old version: 42_smartconfig is then the package's own
// conffile, and stays (the M5 review, A1).
func TestDebHandInstall(t *testing.T) {
	ours := map[string]string{
		"etc/systemd/system/scd.service":                             "[Unit]\nDocumentation=https://github.com/NikhilSah27/SmartConfig\n",
		"etc/systemd/system/sc-boot-seen.service":                    "[Unit]\nDocumentation=https://github.com/NikhilSah27/SmartConfig\n",
		"etc/systemd/system/sc-boot-ok.service":                      "[Unit]\nDocumentation=https://github.com/NikhilSah27/SmartConfig\n",
		"etc/systemd/system/rescue.service.d/50-smartconfig.conf":    "[Service]\nExecStartPre=-/usr/local/sbin/sc status --console\n",
		"etc/systemd/system/emergency.service.d/50-smartconfig.conf": "[Service]\nExecStartPre=-/usr/local/sbin/sc status --console\n",
		"etc/grub.d/42_smartconfig":                                  "#!/bin/sh\n# 42_smartconfig: SmartConfig's part of grub.cfg (M4 plan 3.3, 3.4).\n",
		"usr/local/sbin/sc":                                          "ELF smartconfig/internal/store go1.26.8",
	}
	setup := func(t *testing.T, files map[string]string) (string, string) {
		root, bin := t.TempDir(), t.TempDir()
		for p, data := range files {
			os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
			os.WriteFile(filepath.Join(root, p), []byte(data), 0o755)
		}
		return root, bin
	}
	script := func(t *testing.T, root, bin, name string, args ...string) (string, string) {
		t.Helper()
		code, log, stderr := debScript(t, root, bin, name, nil, args...)
		if code != 0 {
			t.Fatalf("%s %v: exit %d\n%s", name, args, code, stderr)
		}
		return log, stderr
	}
	aside := func(t *testing.T, root, p string) {
		t.Helper()
		fi, err := os.Lstat(filepath.Join(root, p+".dpkg-old"))
		if _, there := os.Lstat(filepath.Join(root, p)); err != nil || there == nil ||
			(fi.Mode()&os.ModeSymlink == 0 && fi.Mode().Perm()&0o111 != 0) {
			t.Errorf("%s not moved aside (or still executable): %v %v", p, fi, err)
		}
	}
	there := func(t *testing.T, root, p string) {
		t.Helper()
		if _, err := os.Lstat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	said := func(p string) string {
		return "smartconfig: the hand-installed /" + p + " is now /" + p + ".dpkg-old\n"
	}
	grubd, sc := "etc/grub.d/42_smartconfig", "usr/local/sbin/sc"

	t.Run("the README's", func(t *testing.T) {
		root, bin := setup(t, ours)
		if _, msg := script(t, root, bin, "preinst", "upgrade", "0.5.0"); msg != "" {
			t.Errorf("an upgrade said %q", msg)
		}
		if _, msg := script(t, root, bin, "preinst", "install", "0.5.0"); msg != "" {
			t.Errorf("a reinstall after a remove said %q", msg) // 42_smartconfig is the package's conffile then
		}
		there(t, root, grubd)
		_, msg := script(t, root, bin, "preinst", "install")
		aside(t, root, grubd)
		if msg != said(grubd) {
			t.Errorf("preinst said %q", msg)
		}
		for p := range ours {
			if p != grubd {
				there(t, root, p) // postinst's, once the package is in
			}
		}
		_, msg = script(t, root, bin, "postrm", "abort-install")
		if fi, err := os.Stat(filepath.Join(root, grubd)); err != nil || fi.Mode().Perm() != 0o755 ||
			msg != "smartconfig: the install failed: /etc/grub.d/42_smartconfig is back\n" {
			t.Errorf("abort-install: %v %v %q", fi, err, msg)
		}
		script(t, root, bin, "preinst", "install")
		if _, msg := script(t, root, bin, "postinst", "configure", "0.5.0"); strings.Contains(msg, "hand-installed") {
			t.Errorf("an upgrade's configure took over: %q", msg) // and leaves the files for the next check
		}
		os.Remove(filepath.Join(bin, "log"))
		log, msg := script(t, root, bin, "postinst", "configure", "")
		for p := range ours {
			aside(t, root, p)
			if p != grubd && !strings.Contains(msg, said(p)) {
				t.Errorf("not said: %s\n%s", p, msg)
			}
		}
		disable := ""
		for _, u := range []string{"scd", "sc-boot-seen", "sc-boot-ok"} {
			disable += "systemctl --root=ROOT disable " + u + ".service\n"
		}
		if !strings.HasPrefix(log, disable+"deb-systemd-helper unmask ") {
			t.Errorf("not disabled before the package's units are enabled:\n%s", log)
		}
	})

	t.Run("not SmartConfig's", func(t *testing.T) {
		root, bin := setup(t, map[string]string{
			"etc/systemd/system/sc-boot-ok.service":                      "[Unit]\nDescription=someone else's\n",
			"etc/systemd/system/emergency.service.d/50-smartconfig.conf": "[Service]\nExecStartPre=-/bin/true\n",
			grubd: "#!/bin/sh\necho someone else's\n",
			sc:    "#!/bin/sh\nexec /opt/sc \"$@\"\n",
		})
		_, msg := script(t, root, bin, "preinst", "install")
		log, msg2 := script(t, root, bin, "postinst", "configure", "")
		for _, w := range []string{
			"/etc/grub.d/42_smartconfig is not SmartConfig's: left; dpkg asks what to do with it\n",
			"/etc/systemd/system/sc-boot-ok.service is not SmartConfig's: left; it overrides the package's sc-boot-ok.service\n",
			"/etc/systemd/system/emergency.service.d/50-smartconfig.conf is not SmartConfig's: left; it overrides the package's drop-in\n",
			"/usr/local/sbin/sc is not SmartConfig's: left; it comes before /usr/sbin/sc in PATH\n",
		} {
			if !strings.Contains(msg+msg2, w) {
				t.Errorf("not said: %q\n%s%s", w, msg, msg2)
			}
		}
		for _, p := range []string{grubd, sc, "etc/systemd/system/sc-boot-ok.service", "etc/systemd/system/emergency.service.d/50-smartconfig.conf"} {
			there(t, root, p)
		}
		if strings.Contains(log, "disable") {
			t.Errorf("disabled someone else's unit:\n%s", log)
		}
	})

	t.Run("42_smartconfig a link", func(t *testing.T) {
		// Moved aside and back as the link; its target keeps its x bits.
		root, bin := setup(t, map[string]string{"opt/42": ours[grubd]})
		os.MkdirAll(filepath.Join(root, "etc/grub.d"), 0o755)
		os.Symlink(filepath.Join(root, "opt/42"), filepath.Join(root, grubd))
		script(t, root, bin, "preinst", "install")
		aside(t, root, grubd)
		script(t, root, bin, "postrm", "abort-install")
		if fi, err := os.Lstat(filepath.Join(root, grubd)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("not back as the link: %v %v", fi, err)
		}
		if fi, err := os.Stat(filepath.Join(root, "opt/42")); err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("the link's target: %v %v", fi, err)
		}
	})

	t.Run("half, sc a link", func(t *testing.T) {
		// M2's and M3's README: sc and scd.service only; sc linked to a
		// build that must keep its x bits.
		root, bin := setup(t, map[string]string{"etc/systemd/system/scd.service": ours["etc/systemd/system/scd.service"],
			"opt/sc": ours[sc]})
		os.MkdirAll(filepath.Join(root, "usr/local/sbin"), 0o755)
		os.Symlink(filepath.Join(root, "opt/sc"), filepath.Join(root, sc))
		script(t, root, bin, "preinst", "install")
		_, msg := script(t, root, bin, "postinst", "configure", "")
		aside(t, root, sc)
		aside(t, root, "etc/systemd/system/scd.service")
		if fi, err := os.Stat(filepath.Join(root, "opt/sc")); err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("the link's target: %v %v", fi, err)
		}
		if msg != said("etc/systemd/system/scd.service")+said(sc) {
			t.Errorf("said %q", msg)
		}
	})
}

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestUnitFile checks scripts/scd.service against plan section 9.
func TestUnitFile(t *testing.T) {
	b, err := os.ReadFile("../../scripts/scd.service")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	unit := "\n" + strings.Join(lines, "\n") + "\n"
	for _, want := range []string{
		"\nType=exec\n", "\nRestart=on-failure\n", "\nWantedBy=multi-user.target\n",
		"\nEnvironment=GOTRACEBACK=none\n", "\nStartLimitBurst=", "\nIOSchedulingClass=",
		"\nExecStart=/usr/local/sbin/sc watch\n", "\nSyslogIdentifier=scd\n",
		"\nExecReload=/bin/kill -HUP $MAINPID\n", "\nAfter=remote-fs.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(unit, "\nIOSchedulingClass=idle") {
		t.Error("idle I/O class: a commit could hold the lock past sc's busy timeout")
	}
	// These could hide /etc, /boot, /root or /home from the watcher, or
	// order other units after scd.
	for _, bad := range []string{"ProtectHome=", "ProtectSystem=", "TemporaryFileSystem=",
		"InaccessiblePaths=", "PrivateTmp=", "Before="} {
		if strings.Contains(unit, "\n"+bad) {
			t.Errorf("unit has %s", bad)
		}
	}
}

// unitLines returns a unit file's lines without blanks and comments,
// between newlines.
func unitLines(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return "\n" + strings.Join(lines, "\n") + "\n"
}

// The boot units (M4 plan 3.2): "seen" early and outside local-fs.target,
// so a boot that fails on a disk is still seen, never on a read-only
// root; the verdict after multi-user.target. systemd-analyze accepts both
// (with /bin/true for sc, which a test machine may not have installed).
func TestBootUnits(t *testing.T) {
	for name, want := range map[string][]string{
		"sc-boot-seen.service": {"\nDefaultDependencies=no\n", "\nAfter=systemd-remount-fs.service\n",
			"\nRequiresMountsFor=/var/lib/smartconfig /usr/local/sbin\n", "\nConditionPathIsReadWrite=/var/lib\n",
			"\nType=oneshot\n", "\nExecStart=/usr/local/sbin/sc boot seen\n", "\nWantedBy=sysinit.target\n", "\nTimeoutStartSec=90s\n",
			"\nIgnoreOnIsolate=yes\n", "\nConditionFileIsExecutable=/usr/local/sbin/sc\n", "\nAfter=boot.mount\n",
			"\nBefore=grub-common.service grub-initrd-fallback.service shutdown.target\n"},
		"sc-boot-ok.service": {"\nConditionPathIsReadWrite=/var/lib\n",
			"\nType=oneshot\n", "\nExecStart=/usr/local/sbin/sc boot verdict\n", "\nWantedBy=multi-user.target\n", "\nTimeoutStartSec=120s\n",
			"\nConditionFileIsExecutable=/usr/local/sbin/sc\n", "\nAfter=multi-user.target sc-boot-seen.service\n"},
	} {
		unit := unitLines(t, name)
		for _, w := range want {
			if !strings.Contains(unit, w) {
				t.Errorf("%s lacks %q", name, strings.TrimSpace(w))
			}
		}
		// Ordered after local-fs.target, seen would miss the boots that fail
		// on a disk; ordered before sysinit.target, a slow seen held up every
		// boot and was killed by its timeout (the chunk B review, in the lab).
		if name == "sc-boot-seen.service" && (strings.Contains(unit, "local-fs.target") || strings.Contains(unit, "Before=sysinit.target")) {
			t.Errorf("%s waits for local-fs.target or holds up sysinit.target", name)
		}
		analyze, err := exec.LookPath("systemd-analyze")
		if err != nil {
			continue
		}
		dir := t.TempDir()
		b, _ := os.ReadFile("../../scripts/" + name)
		os.WriteFile(dir+"/"+name, []byte(strings.ReplaceAll(string(b), "/usr/local/sbin/sc boot", "/bin/true")), 0o644)
		// Exit 0 is not enough: an unknown key is only a warning (a wrong
		// Condition key slipped through that way, the chunk C review).
		if out, err := exec.Command(analyze, "verify", "--man=no", dir+"/"+name).CombinedOutput(); err != nil || len(out) != 0 {
			t.Errorf("%s: systemd-analyze verify: %v\n%s", name, err, out)
		}
	}
}

// The scripts at least parse. The M4 lab's guest scripts are POSIX sh,
// as the guest runs them; its GRUB observers print one echo each, in
// double quotes (GRUB expands no variable in single ones, the step 11
// spike), which grub-script-check accepts.
func TestScriptsParse(t *testing.T) {
	for _, s := range []string{"accept-m2.sh", "accept-m3.sh", "build-sc-m1.sh", "smoke.sh"} {
		if out, err := exec.Command("bash", "-n", "../../scripts/"+s).CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", s, err, out)
		}
	}
	guest, _ := filepath.Glob("../../lab/guest/*.sh")
	if len(guest) == 0 {
		t.Error("no lab/guest/*.sh")
	}
	for _, s := range append(guest, "../../lab/guest/41_sclab", "../../lab/guest/43_sclab") {
		if out, err := exec.Command("sh", "-n", s).CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", s, err, out)
		}
	}
	var cfg strings.Builder
	for _, o := range []struct{ name, want string }{
		{"41_sclab", `echo "sclab: pre platform=${grub_platform} pending=[${smartconfig_pending}] recordfail=[${recordfail}] timeout=[${timeout}] style=[${timeout_style}]"` + "\n"},
		{"43_sclab", `echo "sclab: post timeout=[${timeout}] style=[${timeout_style}]"` + "\n"},
	} {
		out, err := exec.Command("sh", "../../lab/guest/"+o.name).Output()
		if err != nil || string(out) != o.want {
			t.Errorf("%s: %v %q", o.name, err, out)
		}
		cfg.Write(out)
	}
	if check, err := exec.LookPath("grub-script-check"); err == nil {
		f := filepath.Join(t.TempDir(), "grub.cfg")
		os.WriteFile(f, []byte(cfg.String()), 0o644)
		if msg, err := exec.Command(check, f).CombinedOutput(); err != nil {
			t.Errorf("grub-script-check: %v\n%s\n%s", err, msg, cfg.String())
		}
	}
}

// The M4 lab's Python (lab/*.py, standard library only) compiles and its
// unit tests pass: no QEMU, no network (step 11 design, section 7). No
// bytecode is written into the repo.
func TestLabPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	files, _ := filepath.Glob("../../lab/*.py")
	if len(files) == 0 {
		t.Fatal("no lab/*.py")
	}
	env := append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONPYCACHEPREFIX="+t.TempDir())
	compile := exec.Command(py, append([]string{"-B", "-m", "py_compile"}, files...)...)
	compile.Env = env
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("py_compile: %v\n%s", err, out)
	}
	unit := exec.Command(py, "-B", "-m", "unittest", "discover", "-s", "lab")
	unit.Dir, unit.Env = "../..", env
	if out, err := unit.CombinedOutput(); err != nil {
		t.Errorf("python3 -m unittest discover -s lab: %v\n%s", err, out)
	}
}

// labStubs writes the commands of the lab's guest scripts that a test
// must not run for real into dir/bin (SCLAB_TEST's), each logging its
// arguments to dir/log; stubs maps a name to the rest of its script.
func labStubs(t *testing.T, dir string, stubs map[string]string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	for name, body := range stubs {
		script := "#!/bin/sh\necho \"" + name + " $*\" >>\"$SCLAB_TEST/log\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// labBlocks splits facts.sh's output into its blocks: their names in
// order, and each one's rc and lines.
func labBlocks(t *testing.T, out string) (names []string, rc, text map[string]string) {
	t.Helper()
	head := regexp.MustCompile(`^== (\S+) rc=(\d+)$`)
	rc, text = map[string]string{}, map[string]string{}
	cur := ""
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(l, "== ") {
			m := head.FindStringSubmatch(l)
			if m == nil {
				t.Fatalf("header %q", l)
			}
			cur = m[1]
			names, rc[cur] = append(names, cur), m[2]
			continue
		}
		if cur == "" {
			t.Fatalf("a line before the first block: %q", l)
		}
		text[cur] += l + "\n"
	}
	return names, rc, text
}

// facts.sh (lab/guest) prints the blocks lab/e2e.py reads, in each mode,
// with commands that would touch the machine replaced: the names in
// order, each with its exit status; in the rescue shell sc restore only
// on a read-only root, and the hashes before and after, the scratch
// directories left, the menu settings sourced as grub-mkconfig does.
func TestLabGuestFacts(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	for p, data := range map[string]string{
		"etc/fstab":                             "LABEL=cloudimg-rootfs / ext4 defaults 0 1\n",
		"etc/default/grub":                      "GRUB_TIMEOUT=5\nGRUB_CMDLINE_LINUX=\"\"\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash\"\n",
		"etc/default/grub.d/50-cloudimg.cfg":    "GRUB_CMDLINE_LINUX_DEFAULT=\"console=tty1 console=ttyS0\"\nGRUB_TERMINAL=console\n",
		"etc/default/grub.d/60-sclab.cfg":       "GRUB_CMDLINE_LINUX=\"${GRUB_CMDLINE_LINUX:+$GRUB_CMDLINE_LINUX }no_timer_check\"\nGRUB_TIMEOUT=0\n",
		"var/lib/smartconfig/changes.db":        "db",
		"var/lib/smartconfig/boots":             "b1 seen 1\n",
		"usr/local/sbin/sc":                     "sc",
		"run/sc-check-1/x":                      "",
		"etc/systemd/system/scd.service":        "[Unit]\n",
		"boot/grub/grub.cfg":                    "### BEGIN /etc/grub.d/41_sclab ###\necho x\n### END /etc/grub.d/43_sclab ###\n",
		"etc/systemd/system/sc-boot-ok.service": "[Unit]\n",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(data), 0o644)
	}
	labStubs(t, dir, map[string]string{
		"systemctl":  `case "$1" in is-active) echo active ;; is-enabled) echo enabled ;; esac`,
		"journalctl": "", "systemd-analyze": "", "grub-editenv": "", "passwd": "", "dmesg": "",
		// Not this machine's processes: a command line with "p_" in it
		// (an sshd session of backup_user) failed the test below.
		"ps": `echo "    1 Ss   /usr/lib/systemd/systemd-journald"`,
		"sc": `case "$1" in
status) echo "This boot:     3b3b3b3b (rescue), root read-only"; exit 2 ;;
check) echo blocker; exit 2 ;;
restore) echo "sc: it is on a read-only file system: remount it read-write first" >&2; exit 1 ;;
cat) echo "the good fstab" ;;
esac`,
		// / read-only unless SCLAB_RW is set; nothing else mounted.
		"findmnt": `for a; do m=$a; done
[ "$m" = / ] || exit 1
o=ro,relatime; [ -z "$SCLAB_RW" ] || o=rw,relatime
case "$*" in *-P*) echo "SOURCE=\"/dev/vda1\" LABEL=\"cloudimg-rootfs\" FSTYPE=\"ext4\" OPTIONS=\"$o\"" ;; *) echo $o ;; esac`,
	})
	// facts runs facts.sh; all it says is on stdout, but its usage.
	facts := func(env []string, args ...string) (out string, code int) {
		cmd := exec.Command("sh", append([]string{"../../lab/guest/facts.sh"}, args...)...)
		cmd.Env = append(os.Environ(), append(env, "SCLAB_TEST="+dir)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if stderr.Len() > 0 && (code != 2 || !strings.HasPrefix(stderr.String(), "usage: ")) {
			t.Errorf("%v: stderr %q", args, stderr.String())
		}
		return string(b), code
	}
	common := "facts boot-id cmdline consoles "
	for _, tc := range []struct {
		args  []string
		names string
	}{
		{[]string{"normal"}, common + "mounts fstab fstab-sha256 grubenv-stat grubenv grub-defaults root-passwd paths is-enabled active " +
			"show:sc-boot-seen.service show:sc-boot-ok.service show:scd.service dropins units-cat system-running failed " +
			"journal-sc-boot journal-scd boots sc-log-fstab sc-status critical-chain analyze blame grub-cfg end"},
		{[]string{"rescue", "abc123"}, common + "hashes-before mounts active show:sc-boot-seen.service show:sc-boot-ok.service boots " +
			"sc-log-fstab sc-status-console sc-diff sc-cat-good sc-check sc-restore hashes-after leftovers vcs1 end"},
		{[]string{"dump"}, common + "uptime mounts mounts-all system-running targets failed jobs active procs show:sc-boot-seen.service " +
			"show:sc-boot-ok.service show:scd.service grubenv grub-cfg-sclab fstab boots store sc-log-fstab sc-status journal-sc " +
			"journal-warn dmesg end"},
	} {
		out, code := facts(nil, tc.args...)
		names, rc, text := labBlocks(t, out)
		if code != 0 || strings.Join(names, " ") != tc.names {
			t.Errorf("%v: exit %d, blocks\n%s\nnot\n%s", tc.args, code, strings.Join(names, " "), tc.names)
		}
		if strings.Contains(out, "p_") || !strings.HasPrefix(text["facts"], "mode="+tc.args[0]+"\n") {
			t.Errorf("%v:\n%s", tc.args, out)
		}
		if rc["end"] != "0" || text["end"] != "" {
			t.Errorf("%v: end %s %q", tc.args, rc["end"], text["end"])
		}
		switch tc.args[0] {
		case "normal":
			for name, want := range map[string]string{
				"mounts":        "/ SOURCE=\"/dev/vda1\" LABEL=\"cloudimg-rootfs\" FSTYPE=\"ext4\" OPTIONS=\"ro,relatime\"\n/boot -\n/boot/efi -\n/mnt/backup -\n",
				"grub-defaults": "GRUB_DEFAULT=\nGRUB_TIMEOUT=0\nGRUB_TIMEOUT_STYLE=\nGRUB_RECORDFAIL_TIMEOUT=\nGRUB_TERMINAL=console\nGRUB_CMDLINE_LINUX=no_timer_check\nGRUB_CMDLINE_LINUX_DEFAULT=console=tty1 console=ttyS0\nGRUB_DISABLE_RECOVERY=\nGRUB_DISABLE_LINUX_UUID=\n",
				"is-enabled":    "sc-boot-seen.service=enabled\nsc-boot-ok.service=enabled\nscd.service=enabled\n",
				"sc-status":     "This boot:     3b3b3b3b (rescue), root read-only\n",
				"boots":         "b1 seen 1\n",
			} {
				if text[name] != want {
					t.Errorf("normal: %s:\n%s\nnot\n%s", name, text[name], want)
				}
			}
			if rc["sc-status"] != "2" || !strings.Contains(text["paths"], "\n/var/lib/smartconfig dir ") ||
				!strings.Contains(text["paths"], "/usr/local/sbin/sc file 644 ") || !strings.Contains(text["paths"], "\n/etc/grub.d/42_smartconfig -\n") {
				t.Errorf("normal:\n%s", out)
			}
		case "rescue":
			if rc["sc-restore"] != "1" || !strings.Contains(text["sc-restore"], "remount it read-write first") ||
				rc["sc-status-console"] != "2" || rc["sc-check"] != "2" || text["leftovers"] != "/run/sc-check-1\n" ||
				text["hashes-before"] != text["hashes-after"] || !strings.Contains(text["hashes-before"], "/var/lib/smartconfig/changes.db-journal -\n") ||
				text["sc-cat-good"] != fmt.Sprintf("%x\n", sha256.Sum256([]byte("the good fstab\n"))) ||
				!strings.HasPrefix(text["facts"], "mode=rescue\ngood=abc123\nbad=\n") {
				t.Errorf("rescue:\n%s", out)
			}
		}
	}
	// A writable root: sc restore is not run.
	out, _ := facts([]string{"SCLAB_RW=1"}, "rescue", "abc123", "def456")
	_, rc, text := labBlocks(t, out)
	if rc["sc-restore"] != "125" || text["sc-restore"] != "not run: / is not mounted read-only\n" || text["sc-diff"] != "" {
		t.Errorf("writable root:\n%s", out)
	}
	if log, _ := os.ReadFile(filepath.Join(dir, "log")); strings.Count(string(log), "sc restore") != 1 || !strings.Contains(string(log), "sc diff abc123 def456\n") {
		t.Errorf("log:\n%s", log)
	}
	// Usage: a mode, and ids that are ids.
	for _, args := range [][]string{nil, {"rescue"}, {"rescue", "ab;c"}, {"normal", "x"}} {
		if out, code := facts(nil, args...); code != 2 || out != "" {
			t.Errorf("%q: exit %d %q", args, code, out)
		}
	}
}

// grubScript runs scripts/42_smartconfig on a /boot of the test (the
// kernels named, each with an initrd unless its name ends in "!"), with
// grub-mkconfig_lib's device probes replaced, and the environment
// update-grub gives it; it returns the grub.cfg part it prints.
func grubScript(t *testing.T, kernels []string, env ...string) string {
	out, _ := grubScriptErr(t, kernels, env...)
	return out
}

// grubScriptErr is grubScript, with what the script printed on stderr.
func grubScriptErr(t *testing.T, kernels []string, env ...string) (string, string) {
	t.Helper()
	lib := "/usr/share/grub/grub-mkconfig_lib"
	if _, err := os.Stat(lib); err != nil {
		t.Skip("no grub-mkconfig_lib")
	}
	boot := t.TempDir()
	for _, k := range kernels {
		noInitrd := strings.HasSuffix(k, "!")
		k = strings.TrimSuffix(k, "!")
		os.WriteFile(boot+"/vmlinuz-"+k, []byte("k"), 0o644)
		if !noInitrd {
			os.WriteFile(boot+"/initrd.img-"+k, []byte("i"), 0o644)
		}
	}
	pkg := t.TempDir()
	os.WriteFile(pkg+"/grub-mkconfig_lib", []byte(". "+lib+"\n"+
		"prepare_grub_to_access_device () { echo \"search --no-floppy --fs-uuid --set=root BOOTFS\"; }\n"+
		"make_system_path_relative_to_its_root () { echo /boot; }\n"), 0o644)
	cmd := exec.Command("sh", "../../scripts/42_smartconfig")
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "pkgdatadir=" + pkg, "SC_GRUB_BOOT=" + boot,
		"GRUB_DEVICE=/dev/sda2", "GRUB_DEVICE_UUID=sc-no-such-uuid", "GRUB_DEVICE_PARTUUID=sc-no-such-partuuid", "GRUB_FS=ext2",
		"GRUB_CMDLINE_LINUX=net.ifnames=0", "GRUB_CMDLINE_LINUX_DEFAULT=quiet splash console=tty1 console=ttyS0"}, env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("42_smartconfig: %v\n%s", err, stderr.String())
	}
	if check, err := exec.LookPath("grub-script-check"); err == nil {
		f := t.TempDir() + "/grub.cfg"
		os.WriteFile(f, out, 0o644)
		if msg, err := exec.Command(check, f).CombinedOutput(); err != nil {
			t.Errorf("grub-script-check: %v\n%s\n%s", err, msg, out)
		}
	}
	return string(out), stderr.String()
}

// The rescue entry boots the newest kernel that has an initrd, with
// root= as 10_linux gives it, GRUB_CMDLINE_LINUX and the console= settings
// kept, "quiet splash" left out, and the lab's recipe; the menu flag
// follows. A btrfs or ZFS root, or no kernel, gets the flag alone.
func TestGrubScript(t *testing.T) {
	out := grubScript(t, []string{"6.8.0-100-generic", "6.8.0-142-generic", "6.9.0-1-generic!"})
	for _, want := range []string{
		"menuentry 'SmartConfig rescue' --class ubuntu --class gnu-linux --class os --id smartconfig-rescue {\n",
		"\tsearch --no-floppy --fs-uuid --set=root BOOTFS\n",
		"\tlinux\t/boot/vmlinuz-6.8.0-142-generic root=/dev/sda2 net.ifnames=0 console=tty1 console=ttyS0 ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1\n",
		"\tinitrd\t/boot/initrd.img-6.8.0-142-generic\n",
		"if [ \"${smartconfig_pending}\" = \"1\" ] ; then\n\tset timeout_style=menu\n\tif [ \"${timeout}\" = \"0\" ] ; then\n\t\tset timeout=30\n\tfi\nfi\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "quiet") || strings.Contains(out, "splash") {
		t.Errorf("quiet splash kept:\n%s", out)
	}
	for _, env := range []string{"GRUB_FS=btrfs", "GRUB_FS=zfs"} {
		if out := grubScript(t, []string{"6.8.0-142-generic"}, env); strings.Contains(out, "menuentry") || !strings.Contains(out, "smartconfig_pending") {
			t.Errorf("%s:\n%s", env, out)
		}
	}
	if out := grubScript(t, nil); strings.Contains(out, "menuentry") || !strings.Contains(out, "smartconfig_pending") {
		t.Errorf("no kernel:\n%s", out)
	}
}

// The rescue and emergency drop-in runs sc status --console before the
// shell, never stopping it; systemd-analyze accepts rescue.service and
// emergency.service with it (sc replaced by /bin/true, which a test
// machine may not have installed).
func TestRescueDropIn(t *testing.T) {
	unit := unitLines(t, "smartconfig-rescue.conf")
	if unit != "\n[Service]\nExecStartPre=-/usr/local/sbin/sc status --console\n" {
		t.Errorf("drop-in:%q", unit)
	}
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("no systemd-analyze")
	}
	b, _ := os.ReadFile("../../scripts/smartconfig-rescue.conf")
	for _, svc := range []string{"rescue.service", "emergency.service"} {
		src := "/usr/lib/systemd/system/" + svc
		orig, err := os.ReadFile(src)
		if err != nil {
			t.Skip("no " + src)
		}
		dir := t.TempDir()
		os.WriteFile(dir+"/"+svc, orig, 0o644)
		os.MkdirAll(dir+"/"+svc+".d", 0o755)
		os.WriteFile(dir+"/"+svc+".d/50-smartconfig.conf", []byte(strings.ReplaceAll(string(b), "/usr/local/sbin/sc status --console", "/bin/true")), 0o644)
		if out, err := exec.Command(analyze, "verify", "--man=no", dir+"/"+svc).CombinedOutput(); err != nil || len(out) != 0 {
			t.Errorf("%s: %v\n%s", svc, err, out)
		}
	}
}

// The details 10_linux decides: version order (6.8.0-10 after 6.8.0-9),
// root= by UUID or PARTUUID where the link exists, the device when
// GRUB_DISABLE_LINUX_UUID=true (PARTUUID is off by default, as in
// 10_linux); ro wins over an rw in GRUB_CMDLINE_LINUX; GRUB_DISABLE_RECOVERY
// leaves the entry out; a kernel without an initrd is said.
func TestGrubScriptDetails(t *testing.T) {
	if out := grubScript(t, []string{"6.8.0-9-generic", "6.8.0-10-generic"}); !strings.Contains(out, "vmlinuz-6.8.0-10-generic ") {
		t.Errorf("version order:\n%s", out)
	}
	if out := grubScript(t, []string{"6.8.0-1-generic"}, "GRUB_CMDLINE_LINUX=rw"); strings.Index(out, " rw ") < 0 || strings.Index(out, " ro fstab=no ") < strings.Index(out, " rw ") {
		t.Errorf("rw before ro:\n%s", out)
	}
	if out, errOut := grubScriptErr(t, []string{"6.8.0-1-generic"}, "GRUB_DISABLE_RECOVERY=true"); strings.Contains(out, "menuentry") ||
		!strings.Contains(out, "smartconfig_pending") || !strings.Contains(errOut, "GRUB_DISABLE_RECOVERY=true") {
		t.Errorf("recovery off:\n%s\n%s", out, errOut)
	}
	if _, errOut := grubScriptErr(t, []string{"6.8.0-1-generic!"}); !strings.Contains(errOut, "has an initrd") {
		t.Errorf("no initrd, nothing said: %q", errOut)
	}
	// GRUB_DISABLE_LINUX_UUID=true: the device, not "PARTUUID=" (10_linux
	// turns PARTUUID off unless told otherwise).
	if out := grubScript(t, []string{"6.8.0-1-generic"}, "GRUB_DISABLE_LINUX_UUID=true", "GRUB_DEVICE_PARTUUID="); !strings.Contains(out, " root=/dev/sda2 ") {
		t.Errorf("uuid off:\n%s", out)
	}
	// A UUID and a PARTUUID that this machine has.
	link := func(dir string) string {
		entries, _ := os.ReadDir("/dev/disk/" + dir)
		for _, e := range entries {
			if !strings.ContainsAny(e.Name(), " \\") {
				return e.Name()
			}
		}
		return ""
	}
	if u := link("by-uuid"); u != "" {
		if out := grubScript(t, []string{"6.8.0-1-generic"}, "GRUB_DEVICE_UUID="+u); !strings.Contains(out, " root=UUID="+u+" ") {
			t.Errorf("uuid:\n%s", out)
		}
	}
	if p := link("by-partuuid"); p != "" {
		if out := grubScript(t, []string{"6.8.0-1-generic"}, "GRUB_DEVICE_UUID=", "GRUB_DEVICE_PARTUUID="+p, "GRUB_DISABLE_LINUX_PARTUUID=false"); !strings.Contains(out, " root=PARTUUID="+p+" ") {
			t.Errorf("partuuid:\n%s", out)
		}
		// Both links there and GRUB_DISABLE_LINUX_UUID=true: the device,
		// as 10_linux gives it (PARTUUID is off unless asked for).
		if u := link("by-uuid"); u != "" {
			if out := grubScript(t, []string{"6.8.0-1-generic"}, "GRUB_DEVICE_UUID="+u, "GRUB_DEVICE_PARTUUID="+p, "GRUB_DISABLE_LINUX_UUID=true"); !strings.Contains(out, " root=/dev/sda2 ") {
				t.Errorf("uuid off, both links:\n%s", out)
			}
		}
	}
	// A no_entry from the environment does not drop the entry.
	if out := grubScript(t, []string{"6.8.0-1-generic"}, "no_entry=1"); !strings.Contains(out, "menuentry") {
		t.Errorf("no_entry from the environment:\n%s", out)
	}
}

// install.sh (lab/guest) installs what MANIFEST vouches for, each file
// where the README puts it with its mode, then enables the units (the
// boot ones not --now), runs update-grub and passes its stderr on, and
// prints it all as key=value facts; a file that does not match MANIFEST,
// or is not in it, stops it before anything is installed. Never as root:
// should its test hook fail, it would install on this machine.
func TestLabGuestInstall(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("never as root")
	}
	stage := t.TempDir()
	files := map[string]string{"sc": "", "42_smartconfig": "../../scripts/", "41_sclab": "../../lab/guest/", "43_sclab": "../../lab/guest/",
		"scd.service": "../../scripts/", "sc-boot-seen.service": "../../scripts/", "sc-boot-ok.service": "../../scripts/",
		"smartconfig-rescue.conf": "../../scripts/", "install.sh": "../../lab/guest/"}
	sums := map[string]string{}
	var manifest strings.Builder
	for name, from := range files {
		data := []byte("#!/bin/sh\necho a static sc\n")
		if from != "" {
			var err error
			if data, err = os.ReadFile(from + name); err != nil {
				t.Fatal(err)
			}
		}
		os.WriteFile(filepath.Join(stage, name), data, 0o644)
		sums[name] = fmt.Sprintf("%x", sha256.Sum256(data))
		fmt.Fprintf(&manifest, "%s  %s\n", sums[name], name)
	}
	os.WriteFile(filepath.Join(stage, "MANIFEST"), []byte(manifest.String()), 0o644)
	stubs := map[string]string{
		"systemctl": "", "grub-script-check": "", "systemd-analyze": "",
		"update-grub": `echo "Sourcing file /etc/default/grub" >&2
echo "Adding SmartConfig rescue entry: /boot/vmlinuz-6.8.0-142-generic" >&2
echo "to grub.cfg, not stderr"`,
		// install -D -o root -g root -m MODE SRC DEST, without the owner.
		"install": `while [ $# -gt 2 ]; do case "$1" in -m) m=$2; shift 2 ;; -o | -g) shift 2 ;; *) shift ;; esac; done
mkdir -p "$(dirname "$2")" && cp "$1" "$2" && chmod "$m" "$2"`,
	}
	run := func() (dir, out string, code int) {
		dir = t.TempDir()
		labStubs(t, dir, stubs)
		cmd := exec.Command("sh", filepath.Join(stage, "install.sh"))
		cmd.Env = append(os.Environ(), "SCLAB_TEST="+dir)
		b, err := cmd.CombinedOutput()
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return dir, string(b), code
	}
	dir, out, code := run()
	var want []string
	for _, f := range []struct{ name, dest, mode string }{
		{"sc", "/usr/local/sbin/sc", "755"}, {"42_smartconfig", "/etc/grub.d/42_smartconfig", "755"},
		{"41_sclab", "/etc/grub.d/41_sclab", "755"}, {"43_sclab", "/etc/grub.d/43_sclab", "755"},
		{"scd.service", "/etc/systemd/system/scd.service", "644"},
		{"sc-boot-seen.service", "/etc/systemd/system/sc-boot-seen.service", "644"},
		{"sc-boot-ok.service", "/etc/systemd/system/sc-boot-ok.service", "644"},
		{"smartconfig-rescue.conf", "/etc/systemd/system/rescue.service.d/50-smartconfig.conf", "644"},
		{"smartconfig-rescue.conf", "/etc/systemd/system/emergency.service.d/50-smartconfig.conf", "644"},
	} {
		want = append(want, fmt.Sprintf(`file=%s %s \S+:\S+ %s`, regexp.QuoteMeta(f.dest), f.mode, sums[f.name]))
	}
	re := regexp.MustCompile(`(?s)^uid=\d+\nmanifest=ok\n(manifest_out=\S+: OK\n){9}` + strings.Join(want, `\n`) + `\ninstall_rc=0\n` +
		`daemon_reload_rc=0\nenable_rc=0\nenable_scd_rc=0\nupdate_grub_rc=0\nupdate_grub_err=Sourcing file .*\n` +
		`update_grub_err=Adding SmartConfig rescue entry: /boot/vmlinuz-6.8.0-142-generic\ngrub_script_check_rc=0\nverify_rc=0\ndone=1\n$`)
	if code != 0 || !re.MatchString(out) {
		t.Errorf("exit %d:\n%s", code, out)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	if got := regexp.MustCompile(`(?m)^install .*\n`).ReplaceAllString(string(log), ""); got != "systemctl daemon-reload\n"+
		"systemctl enable sc-boot-seen.service sc-boot-ok.service\nsystemctl enable --now scd.service\nupdate-grub \n"+
		"grub-script-check "+dir+"/root/boot/grub/grub.cfg\n"+
		"systemd-analyze verify --man=no sc-boot-seen.service sc-boot-ok.service scd.service rescue.service emergency.service\n" {
		t.Errorf("commands:\n%s", got)
	}

	// A file that MANIFEST does not vouch for: nothing happens.
	sc := filepath.Join(stage, "sc")
	original, _ := os.ReadFile(sc)
	for _, tc := range []struct {
		say          string
		sc, manifest string
	}{
		{"manifest_out=sc: FAILED\n", "another sc\n", manifest.String()},
		{"manifest_out=43_sclab: not in MANIFEST\n", string(original), strings.ReplaceAll(manifest.String(), sums["43_sclab"]+"  43_sclab\n", "")},
	} {
		os.WriteFile(sc, []byte(tc.sc), 0o644)
		os.WriteFile(filepath.Join(stage, "MANIFEST"), []byte(tc.manifest), 0o644)
		dir, out, code := run()
		_, err := os.Stat(filepath.Join(dir, "log"))
		if code != 2 || !strings.HasSuffix(out, "\nmanifest=fail\n") || !strings.Contains(out, tc.say) || err == nil {
			t.Errorf("exit %d, log %v:\n%s", code, err, out)
		}
	}
}

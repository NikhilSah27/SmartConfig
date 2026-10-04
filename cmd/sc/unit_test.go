package main

import (
	"os"
	"os/exec"
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
			"\nRequiresMountsFor=/var/lib/smartconfig\n", "\nConditionPathIsReadWrite=/var/lib\n",
			"\nType=oneshot\n", "\nExecStart=/usr/local/sbin/sc boot seen\n", "\nWantedBy=sysinit.target\n", "\nTimeoutStartSec=90s\n",
			"\nIgnoreOnIsolate=yes\n", "\nConditionPathIsExecutable=/usr/local/sbin/sc\n", "\nAfter=boot.mount\n"},
		"sc-boot-ok.service": {"\nAfter=multi-user.target\n", "\nConditionPathIsReadWrite=/var/lib\n",
			"\nType=oneshot\n", "\nExecStart=/usr/local/sbin/sc boot verdict\n", "\nWantedBy=multi-user.target\n", "\nTimeoutStartSec=120s\n",
			"\nConditionPathIsExecutable=/usr/local/sbin/sc\n"},
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
		if out, err := exec.Command(analyze, "verify", "--man=no", dir+"/"+name).CombinedOutput(); err != nil {
			t.Errorf("%s: systemd-analyze verify: %v\n%s", name, err, out)
		}
	}
}

// The scripts at least parse.
func TestScriptsParse(t *testing.T) {
	for _, s := range []string{"accept-m2.sh", "accept-m3.sh", "build-sc-m1.sh", "smoke.sh"} {
		if out, err := exec.Command("bash", "-n", "../../scripts/"+s).CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", s, err, out)
		}
	}
}

// grubScript runs scripts/42_smartconfig on a /boot of the test (the
// kernels named, each with an initrd unless its name ends in "!"), with
// grub-mkconfig_lib's device probes replaced, and the environment
// update-grub gives it; it returns the grub.cfg part it prints.
func grubScript(t *testing.T, kernels []string, env ...string) string {
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
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("42_smartconfig: %v", err)
	}
	if check, err := exec.LookPath("grub-script-check"); err == nil {
		f := t.TempDir() + "/grub.cfg"
		os.WriteFile(f, out, 0o644)
		if msg, err := exec.Command(check, f).CombinedOutput(); err != nil {
			t.Errorf("grub-script-check: %v\n%s\n%s", err, msg, out)
		}
	}
	return string(out)
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
		"\tlinux\t/boot/vmlinuz-6.8.0-142-generic root=/dev/sda2 ro net.ifnames=0 console=tty1 console=ttyS0 fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1\n",
		"\tinitrd\t/boot/initrd.img-6.8.0-142-generic\n",
		"if [ \"${smartconfig_pending}\" = \"1\" ] ; then\n\tset timeout_style=menu\n",
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
		if out, err := exec.Command(analyze, "verify", "--man=no", dir+"/"+svc).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", svc, err, out)
		}
	}
}

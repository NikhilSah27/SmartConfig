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
			"\nType=oneshot\n", "\nExecStart=/usr/local/sbin/sc boot seen\n", "\nWantedBy=sysinit.target\n", "\nTimeoutStartSec="},
		"sc-boot-ok.service": {"\nAfter=multi-user.target\n", "\nConditionPathIsReadWrite=/var/lib\n",
			"\nType=oneshot\n", "\nExecStart=/usr/local/sbin/sc boot verdict\n", "\nWantedBy=multi-user.target\n", "\nTimeoutStartSec="},
	} {
		unit := unitLines(t, name)
		for _, w := range want {
			if !strings.Contains(unit, w) {
				t.Errorf("%s lacks %q", name, strings.TrimSpace(w))
			}
		}
		// Ordered after local-fs.target, seen would miss the boots that fail
		// on a disk.
		if name == "sc-boot-seen.service" && strings.Contains(unit, "local-fs.target") {
			t.Errorf("%s waits for local-fs.target", name)
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

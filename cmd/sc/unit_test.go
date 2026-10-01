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

// The scripts at least parse.
func TestScriptsParse(t *testing.T) {
	for _, s := range []string{"accept-m2.sh", "build-sc-m1.sh", "smoke.sh"} {
		if out, err := exec.Command("bash", "-n", "../../scripts/"+s).CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", s, err, out)
		}
	}
}

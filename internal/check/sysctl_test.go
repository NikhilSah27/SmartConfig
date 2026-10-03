//go:build linux

package check

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const sysctlDropIn = "/etc/sysctl.d/99-local.conf"

// sysctlCases are fabricated sysctl.d files, the exit code of the real
// sysctl --dry-run -p on each (procps-ng 4.0.4, kernel of the dev VM; the
// golden pair is testdata/sysctl/NAME), and what sc makes of it on a
// machine where only have exists under /proc/sys.
var sysctlCases = []struct {
	name, data string
	code       int
	have       []string
	want, text string
}{
	// Comments, a glob, an exclusion, slashes, blanks, an empty value, a
	// DOS line ending, and a "-" key this kernel does not have, which
	// sysctl reports and systemd-sysctl ignores.
	{name: "good", data: "# comment\n; another\n\n  vm.swappiness = 60\nkernel/printk = 4 4 1 7\nnet.ipv4.conf.*.rp_filter = 2\n" +
		"-net.ipv4.conf.all.rp_filter\n-net.ipv4.sc_no_such_key = 1\nkernel.domainname =\nvm.swappiness=60\r\n", code: 1},
	// Lines 1, 5, 7 and 14 are fine for systemd-sysctl; line 9 is set when
	// sceth9 appears, as the kernel has forwarding for its default.
	{name: "mixed", data: "vm.swappiness = 60\nvm.swappiness 10\nnet.ipv4.sc_no_such_key = 1\nnot an assignment, with hunter2\n" +
		"-net.ipv4.sc_other = 1\nkernel.osrelease = 1\n-kernel.ostype = 1\n= 5\nnet.ipv4.conf.sceth9.forwarding = 1\n" +
		"net.bridge.bridge-nf-call-iptables = 0\nnet.ipv4.sc_no_such_key = 2\n\"vm.swappiness\" = 60\nnet.ipv4.conf.sceth9.sc_nokey = 1\nvm..swappiness = 60\n",
		code: 1, have: []string{"/proc/sys/net/ipv4/conf/default/forwarding"},
		want: "2 sysctl-invalid error\n3 sysctl-unknown-key warning\n4 sysctl-invalid error\n6 sysctl-unknown-key warning\n8 sysctl-invalid error\n" +
			"10 sysctl-unknown-key warning\n11 sysctl-unknown-key warning\n12 sysctl-unknown-key warning\n13 sysctl-unknown-key warning",
		text: "the line is not an assignment (key = value): systemd-sysctl skips it and fails at boot"},
}

func TestSysctlDryRun(t *testing.T) {
	for _, tc := range sysctlCases {
		c, _ := fakeMachine(t, tc.have, "", "", 0)
		args := goldenTool(t, c, "sysctl", "testdata/sysctl/"+tc.name, tc.code)
		rep, err := c.Check(context.Background(), sysctlDropIn, []byte(tc.data))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || len(rep.Notes) != 0 {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q", tc.name, got, tc.want, rep.Notes)
		}
		if tc.text != "" && (len(rep.Findings) == 0 || rep.Findings[0].Text != tc.text) {
			t.Errorf("%s: text %+v, want %q", tc.name, rep.Findings, tc.text)
		}
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok || f.Path != sysctlDropIn || f.Line == 0 || f.Key == "" ||
				strings.Contains(f.Text+f.Raw, "hunter2") || strings.Contains(f.Text+f.Raw, c.Home) {
				t.Errorf("%s: finding %+v", tc.name, f)
			}
		}
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 3 || a[0] != "--dry-run" || a[1] != "-p" || filepath.Base(a[2]) != "99-local.conf" || !strings.HasPrefix(a[2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
	}
	// The texts and the raw line of the mixed case.
	c, _ := fakeMachine(t, sysctlCases[1].have, "", "", 0)
	goldenTool(t, c, "sysctl", "testdata/sysctl/mixed", 1)
	rep, _ := c.Check(context.Background(), sysctlDropIn, []byte(sysctlCases[1].data))
	for i, want := range []string{
		"the line is not an assignment (key = value): systemd-sysctl skips it and fails at boot",
		"net.ipv4.sc_no_such_key is not a setting of this kernel (a typo, or a module that is not loaded): systemd-sysctl skips the line",
		"",
		"kernel.osrelease cannot be set (it is read-only): systemd-sysctl skips the line",
		"the key is empty or leads out of /proc/sys: systemd-sysctl fails at boot",
		"net.bridge.bridge-nf-call-iptables is not a setting of this kernel (a typo, or a module that is not loaded): systemd-sysctl skips the line",
	} {
		if want != "" && (len(rep.Findings) <= i || rep.Findings[i].Text != want) {
			t.Errorf("finding %d: %+v, want %q", i, rep.Findings, want)
		}
	}
	if f := rep.Findings[0]; f.Raw != "sysctl: "+sysctlDropIn+"(2): invalid syntax, continuing..." {
		t.Errorf("raw %q", f.Raw)
	}
	// With sceth9 here now, its unknown setting is unknown.
	c, _ = fakeMachine(t, []string{"/proc/sys/net/ipv4/conf/default/forwarding", "/proc/sys/net/ipv4/conf/sceth9"}, "", "", 0)
	goldenTool(t, c, "sysctl", "testdata/sysctl/mixed", 1)
	rep, _ = c.Check(context.Background(), sysctlDropIn, []byte(sysctlCases[1].data))
	if got := brief(rep.Findings); !strings.Contains(got, "\n9 sysctl-unknown-key warning\n") {
		t.Errorf("interface here:\n%s", got)
	}
}

// A line that only moved is not new to Added; another bad line is.
func TestSysctlKeys(t *testing.T) {
	run := func(line int, data string) []Finding {
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "sysctl", writeGolden(t, "g", "", "sysctl: /SCRATCH/99-local.conf("+strconv.Itoa(line)+"): invalid syntax, continuing...\n"), 0)
		rep, err := c.Check(context.Background(), sysctlDropIn, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	before := run(1, "vm.swappiness 10\n")
	if got := brief(Added(before, run(2, "# moved\nvm.swappiness 10\n"))); got != "" {
		t.Errorf("moved: %q", got)
	}
	if got := brief(Added(before, run(1, "vm.swappiness 20\n"))); got != "1 sysctl-invalid error" {
		t.Errorf("another line: %q", got)
	}
}

// A sysctl that did not check the file says so in a note. procps exits 0
// after an unknown option, printing its usage: that is not a clean run.
func TestSysctlBroken(t *testing.T) {
	data := []byte("vm.swappiness = 60\n")
	for _, tc := range []struct {
		name, script string
		want, note   string
	}{
		{"missing", "", "", "no validator found (sysctl); only sc's own rules ran"},
		{"killed", "kill -9 $$\n", "", "sysctl was killed; only sc's own rules ran"},
		{"hung", "sleep 60\n", "", "sysctl did not finish in time; only sc's own rules ran"},
		{"no --dry-run", "echo \"sysctl: unrecognized option '--dry-run'\" >&2\necho Usage:\nexit 0\n", "",
			"sysctl printed something sc does not understand (sysctl: unrecognized option '--dry-run'); the file may not have been checked"},
		{"cannot open", "echo \"sysctl: cannot open \\\"$3\\\": Permission denied\" >&2\nexit 1\n", "",
			`sysctl printed something sc does not understand (sysctl: cannot open "` + sysctlDropIn + `": Permission denied); the file may not have been checked`},
		{"exit 1, nothing said", "exit 1\n", "", "sysctl exited 1 without a message sc understands; the file was not checked"},
		{"a key sysctl names that is on no line", "echo 'sysctl: cannot stat /proc/sys/net/sc/x: No such file or directory' >&2\nexit 1\n",
			"0 sysctl-unknown-key warning", ""},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		c.Run.Timeout = 300 * time.Millisecond
		if tc.script != "" {
			os.WriteFile(filepath.Join(c.Run.Dirs[0], "sysctl"), []byte("#!/bin/sh\n"+tc.script), 0o755)
		}
		rep, err := c.Check(context.Background(), sysctlDropIn, data)
		if err != nil || brief(rep.Findings) != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s: %q %q %v", tc.name, brief(rep.Findings), rep.Notes, err)
		}
		if tc.want != "" && rep.Findings[0].Text != "net.sc.x is not a setting of this kernel (a typo, or a module that is not loaded): systemd-sysctl skips the line" {
			t.Errorf("%s: %+v", tc.name, rep.Findings[0])
		}
	}
}

func TestSysctlPath(t *testing.T) {
	for key, want := range map[string]string{
		"vm.swappiness":                       "vm/swappiness",
		"kernel/domainname":                   "kernel/domainname",
		"net.ipv4.conf.enp3s0/200.forwarding": "net/ipv4/conf/enp3s0.200/forwarding",
		"net/ipv4/conf/enp3s0.200/forwarding": "net/ipv4/conf/enp3s0.200/forwarding",
		"nodots":                              "nodots",
		"":                                    "",
	} {
		if got := sysctlPath(key); got != want {
			t.Errorf("sysctlPath(%q) = %q, want %q", key, got, want)
		}
	}
	c, _ := fakeMachine(t, []string{"/proc/sys/net/ipv6/neigh/default/retrans_time_ms", "/proc/sys/net/ipv4/conf/default/forwarding"}, "", "", 0)
	for p, want := range map[string]bool{
		"net/ipv6/neigh/wg0/retrans_time_ms": true,
		"net/ipv4/conf/wg0/forwarding":       true,
		"net/ipv4/conf/wg0/sc_nokey":         false,
		"net/ipv4/conf/default/sc_nokey":     false,
		"net/ipv4/conf/all/forwarding":       false,
		"net/bridge/bridge-nf-call-iptables": false,
	} {
		if got := c.ifaceLater(p); got != want {
			t.Errorf("ifaceLater(%s) = %v", p, got)
		}
	}
}

// The real sysctl, where installed, on a fabricated file. Its one valid
// line sets a key to the value it has now, so even a sysctl that ignored
// --dry-run would change nothing; the other lines cannot be set at all.
func TestSysctlRealDryRun(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/sysctl"); err != nil {
		if _, err := os.Stat("/sbin/sysctl"); err != nil {
			t.Skip("no sysctl")
		}
	}
	now, err := os.ReadFile("/proc/sys/vm/swappiness")
	if err != nil {
		t.Skip("no vm.swappiness")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	data := "vm.swappiness = " + strings.TrimSpace(string(now)) + "\nvm.swappiness 10\nnet.ipv4.sc_no_such_key = 1\nkernel.ostype = Linux\n-net.ipv4.sc_other = 1\n"
	rep, err := c.Check(context.Background(), sysctlDropIn, []byte(data))
	if err != nil || brief(rep.Findings) != "2 sysctl-invalid error\n3 sysctl-unknown-key warning\n4 sysctl-unknown-key warning" || len(rep.Notes) != 0 {
		t.Errorf("findings:\n%s\nnotes %q %v", brief(rep.Findings), rep.Notes, err)
	}
	rep, err = c.Check(context.Background(), "/etc/sysctl.conf", []byte("# nothing\nvm.swappiness = "+strings.TrimSpace(string(now))+"\n"))
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 0 {
		t.Errorf("good file: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

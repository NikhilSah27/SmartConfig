//go:build linux

package check

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	netplanPath = "/etc/netplan/60-local.yaml"
	npHead      = "network:\n  version: 2\n"
	npGood      = npHead + "  renderer: networkd\n  ethernets:\n    enp0s3:\n      dhcp4: true\n"
	npBase      = npHead + "  ethernets:\n    enp0s3:\n      dhcp4: false\n"
	npBridge    = npHead + "  bridges:\n    br0:\n      interfaces: [enp0s3]\n      dhcp4: true\n"
	npUnknown   = npHead + "  ethernets:\n    enp0s3:\n      dhcp44: true\n"
	npSecret    = "sc-SECRET-pass"
	npWifi      = npHead + "  wifis:\n    wlan0:\n      dhcp4: true\n      access-points:\n        \"HomeNet\":\n"
	npNoCheck   = "no validator found (/usr/libexec/netplan/generate); only sc's own rules ran"
)

// netplanCases are the generator's outputs, as captured on the dev VM
// (netplan 1.1.2) from fabricated files laid out as checkNetplan lays
// them out, each stream in its own file under testdata/netplan/NAME.
// others are the machine's other netplan files, by the path netplan
// reads them from.
var netplanCases = []struct {
	name   string
	file   string
	others map[string]string
	code   int
	want   string // brief of the findings
	note   string // the notes, joined by "|"
	text   string // the first finding's text
	raw    string // the first finding's raw message
}{
	{name: "good", file: npGood},
	{name: "empty", file: ""},
	// A deprecated key is a GLib warning, with exit 0.
	{name: "gateway4", file: npHead + "  ethernets:\n    enp0s3:\n      addresses: [10.0.0.5/24]\n      gateway4: 10.0.0.1\n"},
	{name: "indent", file: npGood + "     dhcp6: false\n", code: 1,
		want: "7 netplan-invalid blocker", text: "the line's indentation does not fit the lines above it",
		raw: netplanPath + ":7:6: Invalid YAML: inconsistent indentation:"},
	{name: "tab", file: "network:\n\tversion: 2\n", code: 1,
		want: "2 netplan-invalid blocker", text: "the line is indented with a tab, which YAML does not allow"},
	{name: "unknownkey", file: npUnknown, code: 1,
		want: "5 netplan-invalid blocker", text: "netplan does not know the key on this line",
		raw: netplanPath + ":5:7: Error in network definition: unknown key 'dhcp44'"},
	{name: "badvalue", file: npHead + "  ethernets:\n    enp0s3:\n      dhcp4: maybe\n", code: 1,
		want: "5 netplan-invalid blocker", text: "netplan does not accept the value on this line"},
	{name: "badaddr", file: npHead + "  ethernets:\n    enp0s3:\n      addresses: [10.0.0.500/24]\n", code: 1,
		want: "5 netplan-invalid blocker", text: "netplan does not accept the value on this line"},
	{name: "notmap", file: "just a string\n", code: 1,
		want: "1 netplan-invalid blocker", text: "the line has the wrong kind of value: a single value, a list or a mapping"},
	// After a deprecation warning, the error.
	{name: "warnerr", file: npHead + "  ethernets:\n    enp0s3:\n      addresses: [10.0.0.5/24]\n      gateway4: 10.0.0.1\n    enp0s8:\n      dhcp44: true\n", code: 1,
		want: "8 netplan-invalid blocker", text: "netplan does not know the key on this line"},
	// The YAML line the generator quotes holds a password: never kept.
	{name: "wifiindent", file: npWifi + "          mode: ap\n         password: \"" + npSecret + "\"\n", code: 1,
		want: "9 netplan-invalid blocker", text: "the line's indentation does not fit the lines above it",
		raw: netplanPath + ":9:10: Invalid YAML: inconsistent indentation:"},
	// Merged with the other files: a bridge on an interface another file
	// defines is fine, and fails alone.
	{name: "mergeok", file: npBridge, others: map[string]string{"/etc/netplan/50-base.yaml": npBase}},
	{name: "mergealone", file: npBridge, code: 1,
		want: "5 netplan-invalid blocker", text: "the line names an interface that no netplan file defines"},
	{name: "conflict", file: npHead + "  bonds:\n    enp0s3:\n      interfaces: []\n", others: map[string]string{"/etc/netplan/50-base.yaml": npBase}, code: 1,
		want: "4 netplan-invalid blocker", text: "the line defines an interface again, as another type"},
	// Fine alone, not with the other file.
	{name: "alonebad", file: npHead + "  bonds:\n    bond0:\n      interfaces: [enp0s3]\n",
		others: map[string]string{"/etc/netplan/50-base.yaml": npHead + "  ethernets:\n    enp0s3: {}\n  bridges:\n    br0:\n      interfaces: [enp0s3]\n"}, code: 1,
		want: "5 netplan-invalid blocker", text: "the line names an interface that another bond or bridge already has"},
	// The generator stops at another file's error, in any directory.
	{name: "othererr", file: npGood, others: map[string]string{"/etc/netplan/50-base.yaml": npUnknown}, code: 1,
		note: "netplan stops at an error in /etc/netplan/50-base.yaml, line 5: this file was not checked to the end"},
	{name: "liberr", file: npGood, others: map[string]string{"/lib/netplan/00-vendor.yaml": npUnknown}, code: 1,
		note: "netplan stops at an error in /lib/netplan/00-vendor.yaml, line 5: this file was not checked to the end"},
	// About a definition as a whole: the line it starts on.
	{name: "setname", file: npHead + "  ethernets:\n    enp0s3:\n      set-name: lan0\n", code: 1,
		want: "4 netplan-invalid blocker", text: "netplan rejects this device's definition as a whole",
		raw: netplanPath + ": Error in network definition: enp0s3: 'set-name:' requires 'match:' properties"},
	{name: "setnameother", file: npGood,
		others: map[string]string{"/etc/netplan/50-base.yaml": npHead + "  wifis:\n    wlan0:\n      dhcp4: true\n"}, code: 1,
		note: "netplan rejects a definition in /etc/netplan/50-base.yaml: this file was not checked to the end"},
	// A writer's error names no file: with no other file it is this one's.
	{name: "wifishort", file: npWifi + "          password: \"sc-S\"\n", code: 1,
		want: "0 netplan-invalid blocker", text: "netplan reads the file but cannot generate a network configuration from it",
		raw: "ERROR: HomeNet: ASCII passphrase must be between 8 and 63 characters (inclusive)"},
	// With other files the generator runs again without this one, and
	// they give the error alone (the golden tool prints it both times).
	{name: "wifishortother", file: npGood, others: map[string]string{"/etc/netplan/50-base.yaml": npWifi + "          password: \"sc-S\"\n"}, code: 1,
		note: "netplan cannot generate a network configuration from another of its files: this file was not checked to the end"},
}

// netplanMachine is a fakeMachine whose other netplan files are others,
// in a directory standing in for /.
func netplanMachine(t *testing.T, others map[string]string) *Checks {
	t.Helper()
	c, _ := fakeMachine(t, nil, "", "", 0)
	c.netplanRoot = t.TempDir()
	for p, data := range others {
		full := filepath.Join(c.netplanRoot, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// checkNetplanArgs checks that the generator was last given exactly
// --root-dir and the root named last in a scratch directory, and that the
// scratch directory is gone.
func checkNetplanArgs(t *testing.T, c *Checks, args, name, last string) {
	t.Helper()
	b, _ := os.ReadFile(args)
	a := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(a) != 2 || a[0] != "--root-dir" || filepath.Base(a[1]) != last || !strings.HasPrefix(a[1], filepath.Join(c.Home, "tmp", "check-")) {
		t.Errorf("%s: arguments %q", name, a)
	}
	if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
		t.Errorf("%s: scratch copies left: %v", name, left)
	}
}

func TestNetplanGenerate(t *testing.T) {
	for _, tc := range netplanCases {
		c := netplanMachine(t, tc.others)
		args := goldenTool(t, c, "generate", "testdata/netplan/"+tc.name, tc.code)
		rep, err := c.Check(context.Background(), netplanPath, []byte(tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); rep.Checker != "netplan" || got != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		if tc.text != "" && (len(rep.Findings) == 0 || rep.Findings[0].Text != tc.text) {
			t.Errorf("%s: text %+v, want %q", tc.name, rep.Findings, tc.text)
		}
		if tc.raw != "" && (len(rep.Findings) == 0 || rep.Findings[0].Raw != tc.raw) {
			t.Errorf("%s: raw %+v, want %q", tc.name, rep.Findings, tc.raw)
		}
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok {
				t.Errorf("%s: rule %s is not in the table", tc.name, f.Rule)
			}
			for _, s := range []string{f.Raw, f.Text, f.Key} {
				if strings.Contains(s, c.Home) || strings.Contains(s, "\n") {
					t.Errorf("%s: a scratch path or a quoted line: %+v", tc.name, f)
				}
			}
			if strings.Contains(f.Raw, npSecret) || strings.Contains(f.Text, npSecret) || f.Path != netplanPath {
				t.Errorf("%s: %+v", tc.name, f)
			}
		}
		last := "root"
		if tc.name == "wifishortother" {
			last = "others" // the second run
		}
		checkNetplanArgs(t, c, args, tc.name, last)
	}
}

// The root the generator reads: this file under its own name, and every
// other *.yaml netplan reads, in its directory, all 0600. A file of the
// same name is left out: shadowed by this one in /lib/netplan, and in
// /run/netplan shadowing it, which a note says.
func TestNetplanTree(t *testing.T) {
	c := netplanMachine(t, map[string]string{
		"/lib/netplan/00-vendor.yaml":  "lib\n",
		"/lib/netplan/60-local.yaml":   "shadowed\n",
		"/lib/netplan/PLACEHOLDER":     "not yaml\n",
		"/etc/netplan/50-base.yaml":    "base\n",
		"/etc/netplan/.hidden.yaml":    "hidden\n",
		"/etc/netplan/60-local.yaml":   "on disk\n",
		"/etc/netplan/70-other.yml":    "yml\n",
		"/run/netplan/60-local.yaml":   "shadowing\n",
		"/run/netplan/90-runtime.yaml": "run\n",
	})
	list := filepath.Join(t.TempDir(), "list")
	script := "#!/bin/sh\ncd \"$2\" && find . -type f -printf '%m %p ' -exec cat {} \\; | sort > " + list + "\n"
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "generate"), []byte(script), 0o755)
	rep, err := c.Check(context.Background(), netplanPath, []byte("candidate\n"))
	if err != nil || len(rep.Findings) != 0 ||
		strings.Join(rep.Notes, "|") != "netplan reads /run/netplan/60-local.yaml instead of this file while it exists; this file was checked in its place" {
		t.Fatalf("%+v %v", rep, err)
	}
	b, _ := os.ReadFile(list)
	want := "600 ./etc/netplan/50-base.yaml base\n600 ./etc/netplan/60-local.yaml candidate\n" +
		"600 ./lib/netplan/00-vendor.yaml lib\n600 ./run/netplan/90-runtime.yaml run\n"
	if string(b) != want {
		t.Errorf("root:\n%s\nwant:\n%s", b, want)
	}
}

// A file netplan merges that sc cannot read makes the check incomplete,
// and the report says so. A directory named like a file stands in for
// one only root may read, as a test may run as root.
func TestNetplanUnreadable(t *testing.T) {
	c := netplanMachine(t, map[string]string{"/etc/netplan/50-base.yaml": npBase})
	os.Mkdir(filepath.Join(c.netplanRoot, "etc/netplan/40-dir.yaml"), 0o755)
	want := []string{"could not read /etc/netplan/40-dir.yaml (is a directory), which netplan merges with this file: the check is incomplete"}
	if os.Geteuid() != 0 {
		p := filepath.Join(c.netplanRoot, "etc/netplan/50-cloud-init.yaml")
		os.WriteFile(p, []byte(npBase), 0o000)
		want = append(want, "could not read /etc/netplan/50-cloud-init.yaml (permission denied), which netplan merges with this file: the check is incomplete")
	}
	goldenTool(t, c, "generate", "testdata/netplan/mergeok", 0)
	rep, err := c.Check(context.Background(), netplanPath, []byte(npBridge))
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != strings.Join(want, "|") {
		t.Errorf("%+v %v\nwant %q", rep, err, want)
	}
	// A directory netplan reads that sc cannot list.
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Join(c.netplanRoot, "etc/netplan"), 0o000)
		defer os.Chmod(filepath.Join(c.netplanRoot, "etc/netplan"), 0o755)
		rep, err = c.Check(context.Background(), netplanPath, []byte(npBridge))
		if err != nil || len(rep.Notes) != 1 || rep.Notes[0] != "could not list /etc/netplan (permission denied), whose files netplan merges with this one: the check is incomplete" {
			t.Errorf("unlisted: %+v %v", rep, err)
		}
	}
}

// A writer's error, which names no file, is this file's when the other
// files alone do not give it: the generator runs a second time, on them.
func TestNetplanWriterError(t *testing.T) {
	c := netplanMachine(t, map[string]string{"/etc/netplan/50-base.yaml": npBase})
	runs := filepath.Join(t.TempDir(), "runs")
	// The error only when this file is in the root.
	script := fmt.Sprintf("#!/bin/sh\necho \"$2\" >> %s\nif [ -e \"$2/etc/netplan/60-local.yaml\" ]; then\n"+
		"  echo 'ERROR: HomeNet: ASCII passphrase must be between 8 and 63 characters (inclusive)' >&2\n  echo >&2\n  exit 1\nfi\n", runs)
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "generate"), []byte(script), 0o755)
	rep, err := c.Check(context.Background(), netplanPath, []byte(npWifi+"          password: \"sc-S\"\n"))
	if err != nil || brief(rep.Findings) != "0 netplan-invalid blocker" || len(rep.Notes) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	b, _ := os.ReadFile(runs)
	if r := strings.Fields(string(b)); len(r) != 2 || filepath.Base(r[0]) != "root" || filepath.Base(r[1]) != "others" {
		t.Errorf("runs %q", r)
	}
	// The second run did not happen: the error stays this file's.
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "generate"), []byte("#!/bin/sh\n[ \"${2##*/}\" = others ] && kill -9 $$\n"+
		"echo 'ERROR: HomeNet: ASCII passphrase must be between 8 and 63 characters (inclusive)' >&2\nexit 1\n"), 0o755)
	rep, err = c.Check(context.Background(), netplanPath, []byte(npWifi+"          password: \"sc-S\"\n"))
	if err != nil || brief(rep.Findings) != "0 netplan-invalid blocker" || len(rep.Notes) != 0 {
		t.Errorf("second run killed: %+v %v", rep, err)
	}
}

// What the generator says when it did not get as far as the files.
func TestNetplanGenerateFailed(t *testing.T) {
	for _, tc := range []struct {
		name, err string
		code      int
		note      string
	}{
		{"usage", "failed to parse options: Missing argument for --root-dir\n", 1,
			"netplan's generator failed (exit 1) without a message sc understands; the file was not checked"},
		// A shape sc does not know: nothing of it is quoted, as it may be
		// a line of the file.
		{"new shape", "Something about password: " + npSecret + "\n", 1,
			"netplan's generator failed (exit 1) without a message sc understands; the file was not checked"},
		{"silent", "", 2, "netplan's generator failed (exit 2) without a message sc understands; the file was not checked"},
		{"cannot write", "ERROR: cannot create directory /SCRATCH/root//run/systemd/network: Permission denied\n", 1,
			"netplan's generator could not write its output in sc's scratch directory; the file was not checked to the end"},
	} {
		c := netplanMachine(t, nil)
		goldenTool(t, c, "generate", writeGolden(t, "g", "", tc.err), tc.code)
		rep, err := c.Check(context.Background(), netplanPath, []byte(npGood))
		if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s: %+v %v", tc.name, rep, err)
		}
	}
}

// netplan never reads a file whose name starts with a dot: there is
// nothing to check, and the report says so.
func TestNetplanDotFile(t *testing.T) {
	c := netplanMachine(t, nil)
	args := goldenTool(t, c, "generate", "testdata/netplan/unknownkey", 1)
	rep, err := c.Check(context.Background(), "/etc/netplan/.60-local.yaml", []byte(npUnknown))
	if err != nil || rep.Checker != "netplan" || len(rep.Findings) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "netplan does not read this file") {
		t.Errorf("%+v %v", rep, err)
	}
	if _, err := os.Stat(args); err == nil {
		t.Error("the generator ran")
	}
}

// A line that only moved is the same finding; the same error on another
// line is another.
func TestNetplanKeys(t *testing.T) {
	run := func(file string, line int) []Finding {
		c := netplanMachine(t, nil)
		goldenTool(t, c, "generate", writeGolden(t, "g", "",
			fmt.Sprintf("/SCRATCH/root/etc/netplan/60-local.yaml:%d:7: Error in network definition: unknown key 'dhcp44'\n      dhcp44: true\n      ^\n", line)), 1)
		rep, err := c.Check(context.Background(), netplanPath, []byte(file))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	moved := "# a comment\n" + npUnknown
	other := strings.Replace(npUnknown, "enp0s3:\n      dhcp44: true", "enp0s3:\n      dhcp4: true\n    enp0s8:\n      dhcp44: false", 1)
	if got := brief(Added(run(npUnknown, 5), run(moved, 6))); got != "" {
		t.Errorf("moved: %q", got)
	}
	if got := brief(Added(run(npUnknown, 5), run(other, 7))); got != "7 netplan-invalid blocker" {
		t.Errorf("another line: %q", got)
	}
}

// Without the generator, or with one that was killed or hung, the file
// was not checked and the report says so.
func TestNetplanGenerateBroken(t *testing.T) {
	c := netplanMachine(t, nil)
	rep, err := c.Check(context.Background(), netplanPath, []byte(npGood))
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != npNoCheck {
		t.Errorf("no tool: %+v %v", rep, err)
	}
	gen := filepath.Join(c.Run.Dirs[0], "generate")
	os.WriteFile(gen, []byte("#!/bin/sh\necho \"$2/etc/netplan/60-local.yaml:1:1: Invalid YAML: x:\" >&2\nkill -9 $$\n"), 0o755)
	rep, err = c.Check(context.Background(), netplanPath, []byte(npGood))
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != "/usr/libexec/netplan/generate was killed; only sc's own rules ran" {
		t.Errorf("killed: %+v %v", rep, err)
	}
	os.WriteFile(gen, []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err = c.Check(context.Background(), netplanPath, []byte(npGood))
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "did not finish in time") {
		t.Errorf("hung: %+v %v", rep, err)
	}
}

// The real generator, where installed, on fabricated files: the machine's
// own netplan files are never read (netplanRoot stands in for /), so the
// result does not depend on them, as a user or as root.
func TestNetplanRealGenerate(t *testing.T) {
	if _, err := os.Stat(netplanGenerate); err != nil {
		t.Skip("no netplan generator")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome"), netplanRoot: t.TempDir()}
	os.MkdirAll(filepath.Join(c.netplanRoot, "etc/netplan"), 0o755)
	os.WriteFile(filepath.Join(c.netplanRoot, "etc/netplan/50-base.yaml"), []byte(npBase), 0o600)
	for _, tc := range []struct{ file, want string }{
		{npBridge, ""},
		{npUnknown, "5 netplan-invalid blocker"},
		{npWifi + "          password: \"" + npSecret + "\"\n", ""},
	} {
		rep, err := c.Check(context.Background(), netplanPath, []byte(tc.file))
		if err != nil || brief(rep.Findings) != tc.want || len(rep.Notes) != 0 {
			t.Errorf("%q: %+v %v", tc.file, rep, err)
		}
	}
	// Alone, the bridge names an interface nothing defines.
	os.Remove(filepath.Join(c.netplanRoot, "etc/netplan/50-base.yaml"))
	rep, err := c.Check(context.Background(), netplanPath, []byte(npBridge))
	if err != nil || brief(rep.Findings) != "5 netplan-invalid blocker" || len(rep.Notes) != 0 {
		t.Errorf("alone: %+v %v", rep, err)
	}
	if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
		t.Errorf("scratch left: %v", left)
	}
}

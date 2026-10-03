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
	udevPath = "/etc/udev/rules.d/70-local.rules"
	udevGood = `SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="08:00:27:aa:bb:cc", NAME="lan0"` + "\n"
)

// udevCases are udevadm verify --no-style's outputs, as captured on the
// dev VM (systemd 255) from fabricated rules files, each stream in its
// own file under testdata/udevadm/NAME.
var udevCases = []struct {
	name, file string
	code       int
	want       string // brief of the findings
	text, raw  string // the first finding's
}{
	{name: "good", file: udevGood},
	{name: "comment", file: "# only a comment\n"},
	{name: "empty", file: ""},
	// Style remarks are printed, and left out.
	{name: "style", file: `SUBSYSTEM=="net",ACTION=="add",  NAME="lan0"` + "\n"},
	{name: "assignmatch", file: `SUBSYSTEM="net", ACTION=="add", NAME="lan0"` + "\n", code: 1,
		want: "1 udev-invalid error", text: "SUBSYSTEM does not take this operator (== compares, = assigns); udev skips the line",
		raw: udevPath + ":1 Invalid operator for SUBSYSTEM."},
	{name: "unknownkey", file: `SUBSYSTEM=="net", ACTON=="add", NAME="lan0"` + "\n", code: 1,
		want: "1 udev-invalid error", text: "udev does not know the key ACTON; it skips the line"},
	{name: "unterminated", file: `SUBSYSTEM=="net", ACTION=="add", NAME="lan0` + "\n", code: 1,
		want: "1 udev-invalid error", text: `the line is not a list of KEY=="value" pairs (a quote or comma may be missing); udev skips it`},
	{name: "attribute", file: `ATTR=="x", NAME="lan0"` + "\n", code: 1,
		want: "1 udev-invalid error", text: "ATTR has an {attribute} it does not take, or lacks one it needs; udev skips the line"},
	// Two messages about one line, one finding.
	{name: "gotomissing", file: `SUBSYSTEM=="net", GOTO="nowhere"` + "\n" + `ACTION=="add", NAME="lan0"` + "\n", code: 1,
		want: "1 udev-invalid error", text: `no LABEL="nowhere" follows this GOTO in the file; udev ignores the GOTO`,
		raw: udevPath + `:1 GOTO="nowhere" has no matching label, ignoring.; ` + udevPath + ":1 The line has no effect any more, dropping."},
	{name: "multi", file: `ACTON=="add"` + "\n" + `SUBSYSTEM="net"` + "\n" + udevGood + `KERNEL=="sd*", GOTO="nowhere"` + "\n", code: 1,
		want: "1 udev-invalid error\n2 udev-invalid error\n4 udev-invalid error"},
	// Two settings ignored on one line: two findings.
	{name: "owner", file: `KERNEL=="ttyS0", OWNER="sc-nosuchuser", GROUP="sc-nosuchgroup", MODE="0660"` + "\n", code: 1,
		want: "1 udev-invalid error\n1 udev-invalid error", text: `user "sc-nosuchuser" does not exist on this machine; udev ignores the setting`},
	{name: "conflict", file: `ACTION=="add", ACTION=="remove", NAME="lan0"` + "\n", code: 1,
		want: "1 udev-invalid error", text: "the line's conditions contradict each other, so it never applies"},
	{name: "badvalue", file: `ACTION=="add", RUN+="/bin/echo $foo"` + "\n", code: 1,
		want: "1 udev-invalid error", text: "the value of RUN is not valid (invalid substitution type); udev skips the line"},
	// udev names the last line of a continued line.
	{name: "continued", file: `SUBSYSTEM=="net", \` + "\n" + `  ACTON=="add", \` + "\n" + `  NAME="lan0"` + "\n", code: 1,
		want: "3 udev-invalid error", text: "udev does not know the key ACTON; it skips the line"},
	{name: "eof", file: `KERNEL=="sda", \` + "\n", code: 1,
		want: "1 udev-invalid error", text: "the file ends in a line continued with a backslash; udev skips that line"},
	{name: "namek", file: `ACTION=="add", NAME="%k"` + "\n", code: 1,
		want: "1 udev-invalid error", text: "NAME= here would change nothing; udev ignores it"},
	{name: "toolong", file: `KERNEL=="` + strings.Repeat("a", 17000) + `", NAME="x"` + "\n", code: 1,
		want: "0 udev-invalid error", text: "a line is longer than udev reads, so udev skips the whole file"},
	// Warnings: udev still reads the line.
	{name: "dup", file: `SUBSYSTEM=="net", SUBSYSTEM=="net", NAME="lan0"` + "\n", code: 1,
		want: "1 udev-notice warning", text: "the line has the same condition twice"},
	{name: "noeffect", file: `KERNEL=="sda"` + "\n", code: 1,
		want: "1 udev-notice warning", text: "the line has conditions only and does nothing"},
	{name: "assuming", file: `ACTION=="add", NAME+="lan0"` + "\n", code: 1,
		want: "1 udev-notice warning", text: "NAME does not take this operator; udev reads it as ="},
	{name: "oldname", file: `ACTION=="add", SUBSYSTEM=="bus", NAME="x"` + "\n", code: 1,
		want: "1 udev-notice warning", text: `"bus" is an old name for "subsystem"; udev reads it as that`},
	{name: "sysfs", file: `ACTION=="add", ATTRS{device/vendor}=="x", NAME="y"` + "\n", code: 1,
		want: "1 udev-notice warning", text: "the line reads a sysfs path that a later kernel may not have"},
	{name: "twogotos", file: `ACTION=="add", GOTO="a", GOTO="b"` + "\n" + `LABEL="a"` + "\n" + `LABEL="b"` + "\n", code: 1,
		want: "1 udev-notice warning", text: "the line has more than one GOTO; udev ignores all but one"},
	{name: "options", file: `OPTIONS+="bogus_option"` + "\n", code: 1,
		want: "1 udev-notice warning", text: "udev does not know a value of OPTIONS on the line and ignores it"},
}

func TestUdevVerify(t *testing.T) {
	for _, tc := range udevCases {
		c, _ := fakeMachine(t, nil, "", "", 0)
		args := goldenTool(t, c, "udevadm", "testdata/udevadm/"+tc.name, tc.code)
		rep, err := c.Check(context.Background(), udevPath, []byte(tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); rep.Checker != "udev" || got != tc.want || len(rep.Notes) != 0 {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q", tc.name, got, tc.want, rep.Notes)
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
			if strings.Contains(f.Raw, c.Home) || strings.Contains(f.Text, c.Home) || strings.Contains(f.Key, c.Home) || f.Raw == "" {
				t.Errorf("%s: %+v", tc.name, f)
			}
		}
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 3 || a[0] != "verify" || a[1] != "--no-style" || filepath.Base(a[2]) != "70-local.rules" || !strings.HasPrefix(a[2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: arguments %q", tc.name, a)
		}
	}
}

// What udevadm says when it did not check the file.
func TestUdevNotChecked(t *testing.T) {
	summary := func(n int) string {
		return fmt.Sprintf("\n%d udev rules files have been checked.\n  Success: %d\n  Fail:    0\n", n, n)
	}
	for _, tc := range []struct {
		name, out, err string
		code           int
		want, note     string
	}{
		{"unreadable", "\n1 udev rules files have been checked.\n  Success: 0\n  Fail:    1\n",
			"Failed to parse rules file /SCRATCH/70-local.rules: Permission denied\n", 1, "", "udevadm could not read the file (Permission denied)"},
		{"usage", "", "udevadm: unrecognized option '--no-style'\n", 1, "", "udevadm could not check the file (udevadm: unrecognized option '--no-style')"},
		{"silent", "", "", 1, "", "udevadm could not check the file (exit 1)"},
		// A file it passes over is counted as 0 checked, with exit 0.
		{"none checked", summary(0), "", 0, "", "udevadm verify did not report checking the file; it was not checked"},
		{"no summary", "", "", 0, "", "udevadm verify did not report checking the file; it was not checked"},
		{"checked", summary(1), "", 0, "", ""},
		// Something verify learns to say later: an error, as verify failed.
		{"new message", "", "/SCRATCH/70-local.rules:1 Something udev learns to say later.\n/SCRATCH/70-local.rules: udev rules check failed.\n", 1,
			"1 udev-invalid error", ""},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "udevadm", writeGolden(t, "g", tc.out, tc.err), tc.code)
		rep, err := c.Check(context.Background(), udevPath, []byte(udevGood))
		if err != nil || brief(rep.Findings) != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s: %q %q %v", tc.name, brief(rep.Findings), rep.Notes, err)
		}
	}
}

// A line that only moved is the same finding; the same message about
// another line is another.
func TestUdevKeys(t *testing.T) {
	run := func(file string, line int) []Finding {
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "udevadm", writeGolden(t, "g", "", fmt.Sprintf("/SCRATCH/70-local.rules:%d Invalid key 'ACTON'.\n", line)), 1)
		rep, err := c.Check(context.Background(), udevPath, []byte(file))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	bad := `ACTON=="add", NAME="lan0"` + "\n"
	if got := brief(Added(run(bad, 1), run("# moved\n"+bad, 2))); got != "" {
		t.Errorf("moved: %q", got)
	}
	if got := brief(Added(run(bad, 1), run(bad+`ACTON=="remove", NAME="lan1"`+"\n", 2))); got != "2 udev-invalid error" {
		t.Errorf("another line: %q", got)
	}
}

// Without udevadm, or with one that was killed or hung, the file was not
// checked and the report says so.
func TestUdevVerifyBroken(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	rep, err := c.Check(context.Background(), udevPath, []byte(udevGood))
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != "no validator found (udevadm); only sc's own rules ran" {
		t.Errorf("no tool: %+v %v", rep, err)
	}
	tool := filepath.Join(c.Run.Dirs[0], "udevadm")
	os.WriteFile(tool, []byte("#!/bin/sh\necho \"$3:1 Invalid key 'X'.\" >&2\nkill -9 $$\n"), 0o755)
	rep, err = c.Check(context.Background(), udevPath, []byte(udevGood))
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != "udevadm was killed; only sc's own rules ran" {
		t.Errorf("killed: %+v %v", rep, err)
	}
	os.WriteFile(tool, []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err = c.Check(context.Background(), udevPath, []byte(udevGood))
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "udevadm did not finish") {
		t.Errorf("hung: %+v %v", rep, err)
	}
}

// The real udevadm, where installed, on fabricated rules: an unknown key
// is an error, a missing space after a comma nothing.
func TestUdevRealVerify(t *testing.T) {
	if _, err := os.Stat("/usr/bin/udevadm"); err != nil {
		t.Skip("no udevadm")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	for file, want := range map[string]string{
		`SUBSYSTEM=="net",ACTON=="add", NAME="lan0"` + "\n" + udevGood: "1 udev-invalid error",
		`SUBSYSTEM=="net",ACTION=="add", NAME="lan0"` + "\n":           "",
		udevGood: "",
	} {
		rep, err := c.Check(context.Background(), udevPath, []byte(file))
		if err != nil || brief(rep.Findings) != want || len(rep.Notes) != 0 {
			t.Errorf("%q: %+v %v", file, rep, err)
		}
	}
}

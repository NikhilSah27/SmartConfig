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

// goldenTool makes c's only validator a script named tool that records its
// arguments, prints golden.out to stdout and golden.err to stderr and
// exits with code. The golden files were captured from the real tools,
// with the scratch directory they ran on replaced by /SCRATCH; the script
// puts back the directory of the file it is given, as the real tool
// prints the scratch path. It returns the file the arguments go to.
func goldenTool(t *testing.T, c *Checks, tool, golden string, code int) string {
	t.Helper()
	golden, _ = filepath.Abs(golden)
	dir := c.Run.Dirs[0]
	args := filepath.Join(dir, "args")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nfor a in \"$@\"; do f=$a; done\nd=$(dirname \"$f\")\n"+
		"sed \"s|/SCRATCH|$d|g\" %s.out\nsed \"s|/SCRATCH|$d|g\" %s.err >&2\nexit %d\n", args, golden, golden, code)
	if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return args
}

// writeGolden writes a golden pair by hand and returns its path without
// the extension.
func writeGolden(t *testing.T, name, out, errOut string) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, name+".out"), []byte(out), 0o644)
	os.WriteFile(filepath.Join(dir, name+".err"), []byte(errOut), 0o644)
	return filepath.Join(dir, name)
}

const (
	unitDir     = "/etc/systemd/system/"
	goodUnit    = "[Unit]\nDescription=A good service\n\n[Service]\nExecStart=/bin/true\nRestart=always\n\n[Install]\nWantedBy=multi-user.target\n"
	unitNoVerif = "no validator found (systemd-analyze); only sc's own rules ran"
)

// systemd-analyze verify's output, as captured on the dev VM (systemd
// 255) from fabricated units, each stream in its own file.
func TestUnitVerify(t *testing.T) {
	for _, tc := range []struct {
		name, golden, path, unit string
		code                     int
		want, note, text         string
	}{
		{"good", "testdata/systemd-analyze/good", "my.service", goodUnit, 0, "", "", ""},
		// Remarks about a unit that loads are warnings, not load failures
		// (chunk C review): an unsafe user, obsolete and deprecated settings.
		{"remarks", "testdata/systemd-analyze/remarks", "my.service",
			"[Unit]\nDescription=Remarks\n\n[Service]\nExecStart=/bin/true\nUser=nobody\nStandardOutput=syslog\nMemoryLimit=1G\n", 0,
			"6 unit-notice warning\n7 unit-notice warning\n8 unit-notice warning", "", "systemd has a remark about the line; the unit loads"},
		{"a remark and a refusal", "testdata/systemd-analyze/remarkfail", "my.service",
			"[Unit]\nDescription=Remark, then no ExecStart\n\n[Service]\nUser=nobody\n", 1,
			"0 unit-syntax error\n5 unit-notice warning", "", ""},
		{"a remark, then the unit does not load", writeGolden(t, "remarkload", "",
			"/SCRATCH/my.service:5: Something systemd learns to say later\nUnit my.service failed to load properly, please adjust/correct and reload service manager: Bad message\n"),
			"my.service", "[Unit]\nDescription=x\n\n[Service]\nExecStart=/bin/true\n", 1,
			"5 unit-syntax error", "", "systemd cannot read the line; the unit does not load"},
		{"unknown key", "testdata/systemd-analyze/unknownkey", "my.service",
			"[Unit]\nDescription=Unknown key\n\n[Service]\nExecStart=/bin/true\nRestrt=always\n", 0,
			"6 unit-unknown-key warning", "", "Restrt= is not a key systemd knows in [Service]; the line is ignored"},
		{"missing binary", "testdata/systemd-analyze/execmissing", "my.service",
			"[Unit]\nDescription=Missing binary\n\n[Service]\nExecStart=/usr/bin/sc-no-such-binary --flag\n", 1,
			"5 unit-exec-missing error", "", "/usr/bin/sc-no-such-binary does not exist on this machine"},
		{"bad section header", "testdata/systemd-analyze/badheader", "my.service",
			"[Unit]\nDescription=Broken header\n\n[Service\nExecStart=/bin/true\n", 1,
			"4 unit-syntax error", "", "the section header is not valid; the unit does not load"},
		{"no ExecStart", "testdata/systemd-analyze/noexec", "my.service",
			"[Unit]\nDescription=No ExecStart\n\n[Service]\nRestart=always\n", 1,
			"0 unit-syntax error", "", "the service has no ExecStart= line"},
		{"bad value", "testdata/systemd-analyze/badvalue", "my.service",
			"[Unit]\nDescription=Bad value\n\n[Service]\nExecStart=/bin/true\nRestart=sometimes\n", 0,
			"6 unit-unknown-key warning", "", "the value of Restart= is not one systemd accepts; the line is ignored"},
		{"a socket without its service", "testdata/systemd-analyze/socket", "my.socket",
			"[Unit]\nDescription=A socket\n\n[Socket]\nListenStream=12345\n", 1,
			"0 unit-syntax error", "", "the socket's service my.service does not exist on this machine"},
		{"everything at once", "testdata/systemd-analyze/mixed", "my.service",
			"[Unit]\nDescription=x\n\n[Service]\nExecStart=/usr/bin/sc-no-such-binary\nRestrt=always\nRestart=sometimes\n\n[Servce\n", 1,
			"6 unit-unknown-key warning\n7 unit-unknown-key warning\n9 unit-syntax error", "", ""},
		{"an empty file is a mask", "testdata/systemd-analyze/empty", "my.service", "", 1,
			"0 unit-syntax error", "", "the file is empty, so the unit is masked and cannot be started"},
		{"requires a unit that does not exist", "testdata/systemd-analyze/reqmissing", "my.service",
			"[Unit]\nDescription=x\nRequires=sc-no-such-unit.service\nAfter=sc-no-such-unit.service\n\n[Service]\nExecStart=/bin/true\n", 1,
			"0 unit-syntax error", "", "sc-no-such-unit.service, which the unit depends on, does not exist on this machine"},
		// The dependencies' own problems are theirs: one finding, not three.
		{"depends on broken units", "testdata/systemd-analyze/depbroken", "my.service",
			"[Unit]\nDescription=x\nRequires=other.service\nWants=third.service\n\n[Service]\nExecStart=/bin/true\n", 1,
			"0 unit-syntax error", "", "other.service, which the unit depends on, has a bad unit file"},
		{"a mount whose name is wrong", "testdata/systemd-analyze/wrongmount", "data.mount",
			"[Mount]\nWhat=/dev/sda1\nWhere=/mnt/data\n", 1,
			"3 unit-syntax error", "", "the Where= line does not match the unit's name"},
		// A template is verified as an instance, my@i.service.
		{"a template", "testdata/systemd-analyze/template", "my@.service",
			"[Service]\nExecStart=/usr/bin/sc-no-such-binary %i\nRestrt=always\n", 1,
			"2 unit-exec-missing error\n3 unit-unknown-key warning", "", ""},
		{"lines systemd skips", "testdata/systemd-analyze/outside", "my.service",
			"Description=outside\n[Service]\nExecStart=/bin/true\nno equals sign here\n", 0,
			"1 unit-unknown-key warning\n4 unit-unknown-key warning", "", "the line is outside any [Section]; it is ignored"},
		{"a timer with a bad calendar", "testdata/systemd-analyze/badtimer", "my.timer",
			"[Timer]\nOnCalendar=evry day\n", 1,
			"0 unit-syntax error\n2 unit-unknown-key warning", "", "the timer has no valid OnCalendar= or other On...= line"},
		// A man page that is not installed is no problem with the unit,
		// although verify exits 1 for it.
		{"a man page it cannot find", "testdata/systemd-analyze/ownman", "my.service",
			"[Unit]\nDescription=x\nDocumentation=man:sc-no-such-page(8)\n\n[Service]\nExecStart=/bin/true\n", 1, "", "", ""},
		{"a line too long to load", "testdata/systemd-analyze/longline", "my.service",
			"[Service]\nExecStart=/bin/true\nDescription=" + strings.Repeat("x", 2000000) + "\n", 1,
			"0 unit-syntax error", "", "systemd cannot load the unit"},
		// What verify says when it did not get as far as the unit.
		{"usage error", writeGolden(t, "usage", "", "systemd-analyze: unrecognized option '--no-such-option'\n"), "my.service", goodUnit, 1,
			"", "systemd-analyze could not check the unit (systemd-analyze: unrecognized option '--no-such-option')", ""},
		{"unit not found", writeGolden(t, "notfound", "", "my.service: Failed to open /SCRATCH/my.service: Permission denied\nUnit my.service not found.\n"),
			"my.service", goodUnit, 1, "", "systemd-analyze could not load the unit (my.service: Failed to open /etc/systemd/system/my.service: Permission denied)", ""},
		{"unit not found, no reason", writeGolden(t, "notfound2", "", "Unit my.service not found.\n"),
			"my.service", goodUnit, 1, "", "systemd-analyze could not load the unit (Unit my.service not found.)", ""},
		{"no manager", writeGolden(t, "nomanager", "", "Failed to initialize manager: Permission denied\n"), "my.service", goodUnit, 1,
			"", "systemd-analyze could not check the unit (Failed to initialize manager: Permission denied)", ""},
		{"exit 1 and nothing said", writeGolden(t, "silent", "", ""), "my.service", goodUnit, 1,
			"", "systemd-analyze could not check the unit (exit 1)", ""},
		// Something verify learns to say later about a unit that loads.
		{"an unknown message", writeGolden(t, "newmsg", "", "my.service: Something verify says in a later version\n"), "my.service", goodUnit, 1,
			"0 unit-syntax warning", "", "systemd-analyze reports a problem sc has no rule for"},
		{"an unknown refusal", writeGolden(t, "newrefusal", "", "my.service: Type=notify needs NotifyAccess=. Refusing.\nUnit my.service has a bad unit file setting.\n"),
			"my.service", goodUnit, 1, "0 unit-syntax error", "", "systemd refuses the unit as written"},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		args := goldenTool(t, c, "systemd-analyze", tc.golden, tc.code)
		rep, err := c.Check(context.Background(), unitDir+tc.path, []byte(tc.unit))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		if tc.text != "" && (len(rep.Findings) == 0 || rep.Findings[0].Text != tc.text) {
			t.Errorf("%s: text %+v, want %q", tc.name, rep.Findings, tc.text)
		}
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok {
				t.Errorf("%s: rule %s is not in the table", tc.name, f.Rule)
			}
			if strings.Contains(f.Raw, c.Home) || strings.Contains(f.Text, c.Home) || strings.Contains(f.Key, c.Home) {
				t.Errorf("%s: the scratch path leaks: %+v", tc.name, f)
			}
			if f.Rule == "unit-syntax" && f.Raw == "" {
				t.Errorf("%s: no raw message: %+v", tc.name, f)
			}
		}
		// verify was given the scratch copy under the unit's own name,
		// and the copy is gone afterwards.
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 3 || a[0] != "verify" || a[1] != "--man=no" || filepath.Base(a[2]) != tc.path || !strings.HasPrefix(a[2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
		switch tc.name {
		case "bad section header":
			if f := rep.Findings[0]; f.Raw != unitDir+"my.service:4: Invalid section header '[Service'; "+
				"Unit my.service failed to load properly, please adjust/correct and reload service manager: Bad message" {
				t.Errorf("raw: %q", f.Raw)
			}
		case "everything at once":
			if f := rep.Findings[2]; !strings.HasSuffix(f.Raw, "Bad message") || f.Line != 9 {
				t.Errorf("the summary line goes with the syntax finding: %+v", f)
			}
		}
	}
}

// Two different refusals, and the same refusal twice, as Added sees them.
func TestUnitKeys(t *testing.T) {
	run := func(msg string) []Finding {
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "systemd-analyze", writeGolden(t, "g", "", "my.service: "+msg+" Refusing.\nUnit my.service has a bad unit file setting.\n"), 1)
		rep, err := c.Check(context.Background(), unitDir+"my.service", []byte(goodUnit))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	if got := brief(Added(run("One thing is wrong."), run("Another thing is wrong."))); got != "0 unit-syntax error" {
		t.Errorf("got %q", got)
	}
	if got := brief(Added(run("One thing is wrong."), run("One thing is wrong."))); got != "" {
		t.Errorf("the same refusal: %q", got)
	}
	// A second unknown key added above an old one: the new line is blamed.
	unknown := func(data string, lines ...int) []Finding {
		var msgs string
		for _, n := range lines {
			msgs += fmt.Sprintf("/SCRATCH/my.service:%d: Unknown key name 'Restrat' in section 'Service', ignoring.\n", n)
		}
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "systemd-analyze", writeGolden(t, "u", "", msgs), 0)
		rep, err := c.Check(context.Background(), unitDir+"my.service", []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	old := unknown("[Service]\nExecStart=/bin/true\nRestrat=always\n", 3)
	if got := brief(Added(old, unknown("[Service]\nRestrat=no\nExecStart=/bin/true\nRestrat=always\n", 2, 4))); got != "2 unit-unknown-key warning" {
		t.Errorf("added above: %q", got)
	}
}

// Without systemd-analyze, or with one that was killed, hung or printed
// more than sc reads, the unit was not checked and the report says so.
func TestUnitVerifyBroken(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	unit := []byte(goodUnit)
	rep, err := c.Check(context.Background(), unitDir+"my.service", unit)
	if err != nil || rep.Checker != "unit" || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != unitNoVerif {
		t.Errorf("no tool: %+v %v", rep, err)
	}
	tool := filepath.Join(c.Run.Dirs[0], "systemd-analyze")
	os.WriteFile(tool, []byte("#!/bin/sh\necho 'my.service: Command /usr/bin/x is not exec' >&2\nkill -9 $$\n"), 0o755)
	rep, err = c.Check(context.Background(), unitDir+"my.service", unit)
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != "systemd-analyze was killed; only sc's own rules ran" {
		t.Errorf("killed: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	os.WriteFile(tool, []byte("#!/bin/sh\nhead -c 5000 /dev/zero | tr '\\0' x >&2\nexit 1\n"), 0o755)
	c.Run.MaxOut = 1000
	rep, err = c.Check(context.Background(), unitDir+"my.service", unit)
	if err != nil || len(rep.Notes) != 2 || !strings.Contains(rep.Notes[0], "systemd-analyze printed more than sc reads") ||
		!strings.Contains(rep.Notes[1], "could not check the unit") {
		t.Errorf("cut: %q %v", rep.Notes, err)
	}
	c.Run.MaxOut = 0
	os.WriteFile(tool, []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err = c.Check(context.Background(), unitDir+"my.service", unit)
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "systemd-analyze did not finish") {
		t.Errorf("hung: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

// The helpers that find the line a message is about.
func TestUnitLines(t *testing.T) {
	data := []byte("[Service]\nExecStartPre=-/usr/bin/pre arg\nExecStart=@/usr/bin/run name\n Where = /mnt/x\nExecStop=/usr/bin/run\n")
	for _, tc := range []struct {
		key, command string
		want         int
	}{{"Where", "", 4}, {"ExecStart", "", 3}, {"", "/usr/bin/pre", 2}, {"", "/usr/bin/run", 3}, {"", "/usr/bin/nowhere", 0}, {"Nothing", "", 0}} {
		if got := unitLine(data, tc.key, tc.command); got != tc.want {
			t.Errorf("unitLine(%q, %q) = %d, want %d", tc.key, tc.command, got, tc.want)
		}
	}
	if unitKey(data, 4) != "Where" || unitKey(data, 1) != "[Service]" || unitKey(data, 0) != "" || unitKey(data, 99) != "" {
		t.Error("unitKey")
	}
	for file, want := range map[string]string{"/x/my.service": "my.service", "/x/my@.service": "my@i.service", "/x/a@b.service": "a@b.service"} {
		if _, inst := unitName(file); inst != want {
			t.Errorf("unitName(%s) instance = %s, want %s", file, inst, want)
		}
	}
}

// The real systemd-analyze, where installed, on a fabricated unit with a
// typo and a missing command. The stock units it loads as dependencies
// add nothing.
func TestUnitRealVerify(t *testing.T) {
	if _, err := os.Stat("/usr/bin/systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	rep, err := c.Check(context.Background(), unitDir+"sc-test-unit.service",
		[]byte("[Unit]\nDescription=sc test\nRequires=multi-user.target\n\n[Service]\nExecStart=/usr/bin/sc-no-such-binary\nRestrt=always\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := brief(rep.Findings); got != "6 unit-exec-missing error\n7 unit-unknown-key warning" || len(rep.Notes) != 0 {
		t.Errorf("findings:\n%s\nnotes %q", got, rep.Notes)
	}
	rep, err = c.Check(context.Background(), unitDir+"sc-test-unit.service", []byte(goodUnit))
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 0 {
		t.Errorf("good unit: %+v %v", rep, err)
	}
}

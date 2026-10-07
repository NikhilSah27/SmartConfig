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

// dropinTool makes c's systemd-analyze a script for drop-in checks. Each
// run appends a line to the returned log: its unit path, the drop-in's
// place under it, its size, and the arguments. It prints with to stderr
// when the drop-in has content and without when it is empty (the second
// run), FILE standing for the drop-in's scratch path, and exits code. A
// without of "kill" kills the second run.
func dropinTool(t *testing.T, c *Checks, with, without string, code int) string {
	t.Helper()
	dir := c.Run.Dirs[0]
	log := filepath.Join(dir, "runs")
	os.WriteFile(filepath.Join(dir, "with.err"), []byte(with), 0o644)
	os.WriteFile(filepath.Join(dir, "without.err"), []byte(without), 0o644)
	second := fmt.Sprintf(`sed "s|FILE|$f|g" %s >&2`, filepath.Join(dir, "without.err"))
	if without == "kill" {
		second = "kill -9 $$"
	}
	script := fmt.Sprintf("#!/bin/sh\nroot=${SYSTEMD_UNIT_PATH%%:}\nf=$(find \"$root\" -name '*.conf')\n"+
		"printf '%%s|%%s|%%s|%%s\\n' \"$SYSTEMD_UNIT_PATH\" \"${f#$root/}\" \"$(wc -c < \"$f\")\" \"$*\" >> %s\n"+
		"if [ -s \"$f\" ]; then sed \"s|FILE|$f|g\" %s >&2; else %s; fi\nexit %d\n",
		log, filepath.Join(dir, "with.err"), second, code)
	if err := os.WriteFile(filepath.Join(dir, "systemd-analyze"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

// fakeUnitDirs points the unit path at a directory of its own for the
// test, with links as name -> target in it.
func fakeUnitDirs(t *testing.T, links map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, target := range links {
		os.WriteFile(filepath.Join(dir, target), []byte("[Service]\nExecStart=/bin/true\n"), 0o644)
		os.Symlink(target, filepath.Join(dir, name))
	}
	saved := unitDirs
	unitDirs = []string{dir}
	t.Cleanup(func() { unitDirs = saved })
}

const refusal = "%s: Service has more than one ExecStart= setting, which is only allowed for Type=oneshot services. Refusing.\nUnit %s has a bad unit file setting.\n"

// A drop-in is checked with its unit (M3 follow-up 1): systemd-analyze
// verify loads the unit by name with the candidate first on the unit
// path; what it says about the unit as a whole is the drop-in's only when
// a second run, with the drop-in empty, does not say it too.
func TestUnitDropIn(t *testing.T) {
	const typo = "FILE:3: Unknown key name 'Restrat' in section 'Service', ignoring.\n"
	const gone = "my.service: Command /usr/bin/gone is not executable: No such file or directory\n"
	data := "[Service]\nExecStart=/usr/bin/true\nRestrat=always\n"
	for _, tc := range []struct {
		name, path, with, without string
		code                      int
		want, note                string
		runs                      int
		links                     map[string]string
	}{
		{name: "the drop-in's own line, no second run", with: typo, want: "3 unit-unknown-key warning", runs: 1},
		{name: "a refusal the drop-in causes", with: fmt.Sprintf(refusal, "my.service", "my.service"), code: 1,
			want: "2 unit-syntax error", runs: 2},
		{name: "the unit's own problem", with: gone, without: gone, code: 1, runs: 2},
		{name: "all three", with: typo + gone + fmt.Sprintf(refusal, "my.service", "my.service"), without: gone, code: 1,
			want: "2 unit-syntax error\n3 unit-unknown-key warning", runs: 2},
		{name: "a line said twice", with: typo + typo, want: "3 unit-unknown-key warning", runs: 1},
		{name: "no unit of that name", with: "Unit my.service not found.\n", code: 1, want: "0 unit-dropin-orphan warning", runs: 1},
		{name: "no second run", with: typo + fmt.Sprintf(refusal, "my.service", "my.service"), without: "kill", code: 1,
			want: "3 unit-unknown-key warning", runs: 2,
			note: "systemd-analyze did not finish a second run, without the drop-in; what it said about my.service as a whole is left out"},
		// sshd.service is ssh.service on Ubuntu: systemd says ssh.service.
		{name: "an alias", path: unitDir + "sshd.service.d/50-x.conf", with: fmt.Sprintf(refusal, "ssh.service", "ssh.service"), code: 1,
			want: "2 unit-syntax error", runs: 2, links: map[string]string{"sshd.service": "ssh.service"}},
		{name: "another unit's problem", path: unitDir + "sshd.service.d/50-x.conf", with: fmt.Sprintf(refusal, "ssh.service", "ssh.service"), code: 1, runs: 1},
		{name: "a template", path: unitDir + "getty@.service.d/50-x.conf", with: typo, want: "3 unit-unknown-key warning", runs: 1},
		{name: "a drop-in for every foo- unit", path: unitDir + "foo-.service.d/50-x.conf",
			note: "a drop-in for every unit whose name starts with foo-; it was not checked"},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		log := dropinTool(t, c, tc.with, tc.without, tc.code)
		fakeUnitDirs(t, tc.links)
		path := tc.path
		if path == "" {
			path = unitDir + "my.service.d/50-x.conf"
		}
		rep, err := c.Check(context.Background(), path, []byte(data))
		if err != nil || rep.Checker != "unitdropin" {
			t.Fatalf("%s: %+v %v", tc.name, rep, err)
		}
		if got := brief(rep.Findings); got != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok || f.Path != path || strings.Contains(f.Raw+f.Text+f.Key, c.Home) {
				t.Errorf("%s: %+v", tc.name, f)
			}
		}
		b, _ := os.ReadFile(log)
		runs := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(b) == 0 {
			runs = nil
		}
		if len(runs) != tc.runs {
			t.Errorf("%s: %d runs, want %d: %q", tc.name, len(runs), tc.runs, runs)
		}
		// The candidate first, then an empty file in its place, each as
		// UNIT.d/NAME on a unit path of its own under the scratch
		// directory, ending in ":" to keep the stock path; the unit by
		// name, a template by an instance.
		unit := filepath.Base(filepath.Dir(path))
		inst := strings.Replace(strings.TrimSuffix(unit, ".d"), "@.", "@i.", 1)
		for i, r := range runs {
			f := strings.Split(r, "|")
			size := fmt.Sprint(len(data))
			if i == 1 {
				size = "0"
			}
			if len(f) != 4 || !strings.HasPrefix(f[0], filepath.Join(c.Home, "tmp", "check-")) || !strings.HasSuffix(f[0], ":") ||
				f[1] != unit+"/50-x.conf" || f[2] != size || f[3] != "verify --man=no "+inst {
				t.Errorf("%s: run %d: %q", tc.name, i, f)
			}
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
	}
}

// unitAlias follows a link on the unit path to a unit of another name
// only.
func TestUnitAlias(t *testing.T) {
	fakeUnitDirs(t, map[string]string{"sshd.service": "ssh.service", "same.service": "same.service.real"})
	os.Symlink("/dev/null", filepath.Join(unitDirs[0], "masked.service"))
	for name, want := range map[string]string{"sshd.service": "ssh.service", "ssh.service": "", "masked.service": "",
		"same.service": "", "nothere.service": ""} {
		if got := unitAlias(name); got != want {
			t.Errorf("unitAlias(%s) = %q, want %q", name, got, want)
		}
	}
}

// The real systemd-analyze, where installed, on drop-ins for the journal's
// own unit, which every systemd machine has, and for a unit no machine has.
func TestUnitDropInRealVerify(t *testing.T) {
	if _, err := os.Stat("/usr/bin/systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze")
	}
	if _, err := os.Stat("/usr/lib/systemd/system/systemd-journald.service"); err != nil {
		t.Skip("no systemd-journald.service")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	journald := unitDir + "systemd-journald.service.d/50-sc-test.conf"
	for _, tc := range []struct{ name, path, data, want string }{
		{"a second ExecStart and a typo", journald, "[Service]\nExecStart=/usr/bin/true\nRestrt=always\n", "2 unit-syntax error\n3 unit-unknown-key warning"},
		{"good", journald, "[Service]\nExecStart=\nExecStart=/usr/lib/systemd/systemd-journald\n", ""},
		{"no such unit", unitDir + "sc-no-such-unit.service.d/50-x.conf", "[Service]\nRestart=always\n", "0 unit-dropin-orphan warning"},
	} {
		rep, err := c.Check(context.Background(), tc.path, []byte(tc.data))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || len(rep.Notes) != 0 {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q", tc.name, got, tc.want, rep.Notes)
		}
	}
}

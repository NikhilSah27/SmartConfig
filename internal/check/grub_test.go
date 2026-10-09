//go:build linux

package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	grubDefault = "GRUB_DEFAULT=0\nGRUB_TIMEOUT=0\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash\"\nGRUB_CMDLINE_LINUX=\"\"\n"
	grubCfg     = "set default=\"0\"\nmenuentry 'Ubuntu' {\n\tlinux\t/boot/vmlinuz root=/dev/sda1 ro\n\tinitrd\t/boot/initrd.img\n}\n"
)

// sh -n's output, as captured on the dev VM (dash 0.5.12) from fabricated
// files. dash stops at the first error.
func TestShSyntax(t *testing.T) {
	for _, tc := range []struct {
		name, golden, file string
		code               int
		want, note, text   string
	}{
		{"good", "testdata/sh/good", grubDefault, 0, "", "", ""},
		{"unterminated quote", "testdata/sh/unterminated", strings.Replace(grubDefault, `splash"`, "splash", 1), 2,
			"3 grub-default-syntax blocker", "", "a quote on this line is never closed"},
		{"a stray parenthesis", "testdata/sh/paren", strings.Replace(grubDefault, "TIMEOUT=0", "TIMEOUT=0)", 1), 2,
			"2 grub-default-syntax blocker", "", `the line is not valid shell (")" unexpected)`},
		{"an if without fi", "testdata/sh/ifnofi", "GRUB_DEFAULT=0\nif true; then\nGRUB_TIMEOUT=0\n", 2,
			"3 grub-default-syntax blocker", "", `the line is not valid shell (end of file unexpected (expecting "fi"))`},
		{"cannot open", writeGolden(t, "cannotopen", "", "sh: 0: cannot open /SCRATCH/grub: No such file\n"), grubDefault, 2,
			"", "sh could not check the file said: sh: 0: cannot open /etc/default/grub: No such file", ""},
		{"exit 2 and nothing said", writeGolden(t, "silent", "", ""), grubDefault, 2,
			"", "sh could not check the file (exit 2, no message)", ""},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		args := goldenTool(t, c, "sh", tc.golden, tc.code)
		rep, err := c.Check(context.Background(), "/etc/default/grub", []byte(tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || notesSaid(rep) != tc.note || rep.Checker != "shsyntax" {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		if tc.text != "" && (len(rep.Findings) == 0 || rep.Findings[0].Text != tc.text) {
			t.Errorf("%s: text %+v, want %q", tc.name, rep.Findings, tc.text)
		}
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok {
				t.Errorf("%s: rule %s is not in the table", tc.name, f.Rule)
			}
			if f.Path != "/etc/default/grub" || strings.Contains(f.Raw, c.Home) || !strings.HasPrefix(f.Raw, "/etc/default/grub: ") || f.Key == "" {
				t.Errorf("%s: %+v", tc.name, f)
			}
		}
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 2 || a[0] != "-n" || filepath.Base(a[1]) != "grub" || !strings.HasPrefix(a[1], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
	}
}

// The same error on a different line is a new finding to Added; the same
// line moved is not.
func TestShSyntaxKeys(t *testing.T) {
	run := func(line int, file string) []Finding {
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "sh", writeGolden(t, "g", "", "/SCRATCH/grub: "+string(rune('0'+line))+": Syntax error: Unterminated quoted string\n"), 2)
		rep, err := c.Check(context.Background(), "/etc/default/grub", []byte(file))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	before := run(1, "A=\"x\nB=1\n")
	if got := brief(Added(before, run(2, "A=\"x\"\nB=\"y\n"))); got != "2 grub-default-syntax blocker" {
		t.Errorf("another line: %q", got)
	}
	if got := brief(Added(before, run(2, "# moved\nA=\"x\nB=1\n"))); got != "" {
		t.Errorf("moved: %q", got)
	}
}

// Without sh, or with one that was killed or hung, the file was not
// checked and the report says so.
func TestShSyntaxBroken(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	file := []byte(grubDefault)
	rep, err := c.Check(context.Background(), "/etc/default/grub", file)
	if err != nil || len(rep.Findings) != 0 || notesSaid(rep) != "no validator found (sh); only sc's own rules ran" {
		t.Errorf("no tool: %+v %v", rep, err)
	}
	tool := filepath.Join(c.Run.Dirs[0], "sh")
	os.WriteFile(tool, []byte("#!/bin/sh\nkill -9 $$\n"), 0o755)
	rep, err = c.Check(context.Background(), "/etc/default/grub", file)
	if err != nil || len(rep.Findings) != 0 || notesSaid(rep) != "sh was killed; only sc's own rules ran" {
		t.Errorf("killed: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	os.WriteFile(tool, []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err = c.Check(context.Background(), "/etc/default/grub", file)
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "sh did not finish") {
		t.Errorf("hung: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

// The real sh (always installed) on a fabricated file.
func TestShSyntaxReal(t *testing.T) {
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	rep, err := c.Check(context.Background(), "/etc/default/grub", []byte("GRUB_DEFAULT=0\nGRUB_CMDLINE_LINUX=\"quiet\nGRUB_TIMEOUT=0\n"))
	if err != nil {
		t.Fatal(err)
	}
	// dash says line 4, the end of the file: the quote opens on line 2.
	if got := brief(rep.Findings); got != "2 grub-default-syntax blocker" || len(rep.Notes) != 0 {
		t.Errorf("findings:\n%s\nnotes %q", got, rep.Notes)
	}
	if rep, err = c.Check(context.Background(), "/etc/default/grub", []byte(grubDefault)); err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 0 {
		t.Errorf("good file: %+v %v", rep, err)
	}
}

// /etc/default/grub.d/*.cfg are read by update-grub as /etc/default/grub
// is, after it (the cloud image keeps its settings in one): the same check
// and rules (M5 follow-up 4, found by the M6 pilot as G08). Other names in
// grub.d are not sourced, and not checked.
func TestShSyntaxGrubD(t *testing.T) {
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	p := "/etc/default/grub.d/50-cloudimg-settings.cfg"
	rep, err := c.Check(context.Background(), p, []byte("GRUB_CMDLINE_LINUX_DEFAULT=\"console=tty1 console=ttyS0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := brief(rep.Findings); got != "1 grub-default-syntax blocker" || rep.Checker != "shsyntax" || rep.Findings[0].Path != p {
		t.Errorf("unclosed quote: %s (checker %q) %+v", got, rep.Checker, rep.Findings)
	}
	rep, err = c.Check(context.Background(), p, []byte("GRUB_TIMEOUT = 5\n"))
	if err != nil || brief(rep.Findings) != "1 grub-default-syntax blocker" {
		t.Errorf("spaces around =: %+v %v", rep, err)
	}
	for _, other := range []string{"/etc/default/grub.d/50-x.cfg.bak", "/etc/default/grub.d/sub/x.cfg"} {
		if g := c.GraphInUse().Checker(other); g != "" {
			t.Errorf("%s: checker %q", other, g)
		}
	}
}

// grub-script-check's output, as captured on the dev VM (GRUB 2.12) from
// fabricated files: all of it on stderr.
func TestGrubCfg(t *testing.T) {
	for _, tc := range []struct {
		name, golden, path, file string
		code                     int
		want, note, text         string
	}{
		{"good", "testdata/grub-script-check/good", "grub.cfg", grubCfg, 0, "", "", ""},
		{"an unclosed menuentry", "testdata/grub-script-check/unclosed", "grub.cfg",
			strings.Replace(grubCfg, "}\n", "set timeout=5\n", 1), 1,
			"5 grubcfg-syntax blocker", "", "GRUB cannot read the menu past this line"},
		// Commands are looked up when GRUB runs them: a misspelt one
		// is not a syntax error.
		{"an unknown command", "testdata/grub-script-check/unknowncmd", "grub.cfg", strings.Replace(grubCfg, "linux", "lnux", 1), 0, "", "", ""},
		{"a stray brace", "testdata/grub-script-check/extra", "custom.cfg", "set timeout=5\n}\nmenuentry 'A' {\n  linux /x\n}\n", 1,
			"2 grubcfg-syntax blocker", "", ""},
		// An empty custom.cfg is nothing; an empty grub.cfg is no menu.
		{"an empty custom.cfg", "testdata/grub-script-check/empty", "custom.cfg", "", 1, "", "", ""},
		{"an empty grub.cfg", "testdata/grub-script-check/empty", "grub.cfg", "", 1,
			"0 grubcfg-syntax blocker", "", "the file has no commands, so GRUB has no menu to boot from"},
		{"cannot open", writeGolden(t, "cannotopen", "", "cannot open `/SCRATCH/grub.cfg': No such file or directoryUsage: grub-script-check [OPTION...] [PATH]\n"+
			"Try 'grub-script-check --help' or 'grub-script-check --usage' for more\ninformation.\n"), "grub.cfg", grubCfg, 1,
			"", "grub-script-check could not check the file (exit 1) said: cannot open `/boot/grub/grub.cfg': No such file or directoryUsage: grub-script-check [OPTION...] [PATH]", ""},
		{"exit 1 and nothing said", writeGolden(t, "silent", "", ""), "grub.cfg", grubCfg, 1,
			"", "grub-script-check could not check the file (exit 1, no message)", ""},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		args := goldenTool(t, c, "grub-script-check", tc.golden, tc.code)
		rep, err := c.Check(context.Background(), "/boot/grub/"+tc.path, []byte(tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || notesSaid(rep) != tc.note || rep.Checker != "grubcfg" {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		if tc.text != "" && (len(rep.Findings) == 0 || rep.Findings[0].Text != tc.text) {
			t.Errorf("%s: text %+v, want %q", tc.name, rep.Findings, tc.text)
		}
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok {
				t.Errorf("%s: rule %s is not in the table", tc.name, f.Rule)
			}
			if f.Path != "/boot/grub/"+tc.path || strings.Contains(f.Raw, c.Home) {
				t.Errorf("%s: %+v", tc.name, f)
			}
		}
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 1 || filepath.Base(a[0]) != tc.path || !strings.HasPrefix(a[0], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
		if tc.name == "an unclosed menuentry" {
			if f := rep.Findings[0]; f.Raw != "error: out of memory.; error: syntax error.; error: Incorrect command.; error: syntax error.; Syntax error at line 5" || f.Key == "" {
				t.Errorf("raw: %+v", f)
			}
		}
	}
}

// Without grub-script-check, or with one that was killed or hung, the
// file was not checked and the report says so.
func TestGrubCfgBroken(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	file := []byte(grubCfg)
	rep, err := c.Check(context.Background(), "/boot/grub/grub.cfg", file)
	if err != nil || len(rep.Findings) != 0 || notesSaid(rep) != "no validator found (grub-script-check); only sc's own rules ran" {
		t.Errorf("no tool: %+v %v", rep, err)
	}
	tool := filepath.Join(c.Run.Dirs[0], "grub-script-check")
	os.WriteFile(tool, []byte("#!/bin/sh\necho 'error: syntax error.' >&2\nkill -9 $$\n"), 0o755)
	rep, err = c.Check(context.Background(), "/boot/grub/grub.cfg", file)
	if err != nil || len(rep.Findings) != 0 || notesSaid(rep) != "grub-script-check was killed; only sc's own rules ran" {
		t.Errorf("killed: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	os.WriteFile(tool, []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err = c.Check(context.Background(), "/boot/grub/grub.cfg", file)
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "grub-script-check did not finish") {
		t.Errorf("hung: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

// The real grub-script-check, where installed, on a fabricated file.
func TestGrubCfgReal(t *testing.T) {
	if _, err := os.Stat("/usr/bin/grub-script-check"); err != nil {
		t.Skip("no grub-script-check")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	rep, err := c.Check(context.Background(), "/boot/grub/custom.cfg", []byte("menuentry 'A' {\n  linux /x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := brief(rep.Findings); got != "2 grubcfg-syntax blocker" || len(rep.Notes) != 0 {
		t.Errorf("findings:\n%s\nnotes %q", got, rep.Notes)
	}
	if rep, err = c.Check(context.Background(), "/boot/grub/grub.cfg", []byte(grubCfg)); err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 0 {
		t.Errorf("good file: %+v %v", rep, err)
	}
}

// Two mistakes sh -n accepts and update-grub stops at (set -e, then
// ". FILE": checked with dash, exit 127): spaces around =, and a value
// with a space but no quotes. Quoted, escaped and multi-line values, and
// comments, are fine.
func TestShAssignments(t *testing.T) {
	for _, tc := range []struct{ data, want string }{
		{"GRUB_TIMEOUT=5\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash\"\n", ""},
		{"GRUB_CMDLINE_LINUX_DEFAULT=quiet splash\n", "1 grub-default-syntax blocker"},
		{"GRUB_TIMEOUT = 5\n", "1 grub-default-syntax blocker"},
		{"  GRUB_TIMEOUT =5\n", "1 grub-default-syntax blocker"},
		{"GRUB_TIMEOUT= 5\n", "1 grub-default-syntax blocker"},
		{"GRUB_TIMEOUT=5 # five seconds\nGRUB_X=a;GRUB_Y=b\n", ""},
		// A value with $ or ` is not judged (GRUB_W=$A b does fail).
		{"GRUB_X=a\\ b\nGRUB_Y='a b'\nGRUB_Z=a\"b c\"\n", ""},
		{"GRUB_DISTRIBUTOR=`( . /etc/os-release; echo ${NAME:-Ubuntu} ) 2>/dev/null || echo Ubuntu`\n", ""},
		{"GRUB_CMDLINE_LINUX=\"a\nB = c\nd\"\nGRUB_TIMEOUT = 5\n", "4 grub-default-syntax blocker"},
		{"# GRUB_TIMEOUT = 5\n#GRUB_X=a b\n", ""},
		{"GRUB_X='it''s'\nGRUB_Y=1 2\n", "2 grub-default-syntax blocker"},
	} {
		if got := brief(shAssignments([]byte(tc.data))); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.data, got, tc.want)
		}
		// dash agrees: a line flagged makes ". FILE" under set -e fail.
		f := filepath.Join(t.TempDir(), "grub")
		os.WriteFile(f, []byte(tc.data), 0o644)
		err := exec.Command("/bin/sh", "-c", "set -e; . "+f+" >/dev/null 2>&1").Run()
		if (err != nil) != (tc.want != "") {
			t.Errorf("%q: dash says %v", tc.data, err)
		}
	}
}

// The line of a quote a file never closes (M3 follow-up 8): dash reports
// it at the end of the file.
func TestShQuoteOpen(t *testing.T) {
	for _, tc := range []struct {
		data string
		want int
	}{
		{"A=0\nB=\"x\nC=\"\"\n", 2},                // C's first quote would close B's
		{"A=\"x\ny\"\nB='z\n", 3},                  // a value over two lines, then the open one
		{"A=\"it's\"\nB='a\"b'\nC=\"q\\\"\n", 3},   // ' inside ", " inside ', \" inside "
		{"# don't\nA=x # it's\nB=a#'b\nC=\"\n", 3}, // comments; a # in a word is not one
		{"A=\\\"x\nB=`date\n", 2},                  // \" outside quotes; a backquote
		{"A=\"x\\\ny\nB=1\n", 1},                   // a backslash and newline inside quotes
		{"A=0\nB=\"x\"\n", 0},
		{"A=\"x\nB='y\nC=\"z\"\n", 1},   // the next open line does not close it
		{"A=1;# it's\nB=2 # it's\n", 0}, // a # after ; or a space starts a comment
	} {
		if got := shQuoteOpen([]byte(tc.data)); got != tc.want {
			t.Errorf("%q: %d, want %d", tc.data, got, tc.want)
		}
	}
}

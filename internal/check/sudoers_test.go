//go:build linux

package check

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeVisudo is a fakeMachine whose only validator is a visudo that
// prints golden.out and golden.err with @DIR@ replaced by the directory
// of the file it was given, as the real visudo names the file it reads
// (the goldens were captured from it on fabricated files, with that
// directory rewritten), and exits with code. The file being checked does
// not exist on disk: sc edit creating it.
func fakeVisudo(t *testing.T, have []string, golden string, code int) (*Checks, string) {
	t.Helper()
	c, args := fakeMachine(t, have, "", "", 0)
	golden, _ = filepath.Abs(golden)
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > %s\nd=$(dirname \"$3\")\nsed \"s|@DIR@|$d|g\" %s.out\nsed \"s|@DIR@|$d|g\" %s.err >&2\nexit %d\n", args, golden, golden, code)
	if err := os.WriteFile(filepath.Join(c.Run.Dirs[0], "visudo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c.lstat = func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
	return c, args
}

const (
	sudoersGood  = "Defaults env_reset\nroot ALL=(ALL:ALL) ALL\n%sudo ALL=(ALL:ALL) ALL\n"
	sudoersOne   = "Defaults env_reset\nvboxuser ALL=(ALL NOPASSWD ALL\n"
	sudoersThree = "Defaults env_reset\nvboxuser ALL=(ALL NOPASSWD ALL\nroot ALL=(ALL:ALL) ALL\nalice ALL = = ALL\nbob\n"
	noteOther    = "visudo reports a problem in /etc/sudoers.d/10-bad, line 1, which this file includes; sc check /etc/sudoers.d/10-bad shows it"
)

// visudo's output, as captured on the dev VM from fabricated files (each
// stream in its own file, as the runner reads them). The .root goldens
// include a directory of fabricated drop-ins, renamed /etc/sudoers.d as
// root would see it; as a user visudo cannot open the real drop-ins.
func TestSudoersVisudo(t *testing.T) {
	dir := t.TempDir()
	write := func(name, out, errOut string) string {
		os.WriteFile(filepath.Join(dir, name+".out"), []byte(out), 0o644)
		os.WriteFile(filepath.Join(dir, name+".err"), []byte(errOut), 0o644)
		return filepath.Join(dir, name)
	}
	for _, tc := range []struct {
		name, golden, path, sudoers string
		have                        []string
		code                        int
		want, note, text, raw       string
	}{
		{name: "good", golden: "testdata/visudo/good", sudoers: sudoersGood},
		{name: "one error", golden: "testdata/visudo/one", sudoers: sudoersOne, code: 1,
			want: "2 sudoers-syntax blocker", text: "syntax error at column 19",
			raw: "/etc/sudoers:2:19: syntax error\nvboxuser ALL=(ALL NOPASSWD ALL\n                  ^~~~~~~~"},
		{name: "three errors", golden: "testdata/visudo/three", sudoers: sudoersThree, code: 1,
			want: "2 sudoers-syntax blocker\n4 sudoers-syntax blocker\n5 sudoers-syntax blocker"},
		// What sudo logs and goes on without (sudoers(5): "problem with
		// defaults entries").
		{name: "unknown Defaults", golden: "testdata/visudo/defaults", sudoers: "Defaults env_rest\nDefaults nosuchthing=3\nroot ALL=(ALL:ALL) ALL\n", code: 1,
			want: "1 sudoers-syntax error\n2 sudoers-syntax error", text: `unknown Defaults option "env_rest"`,
			raw: "/etc/sudoers:1:18: unknown defaults entry \"env_rest\""},
		{name: "bad Defaults value", golden: "testdata/visudo/badvalue", sudoers: "Defaults timestamp_timeout=abc\nroot ALL=(ALL:ALL) ALL\n", code: 1,
			want: "1 sudoers-syntax error", text: `invalid value for Defaults option "timestamp_timeout"`},
		{name: "alias twice", golden: "testdata/visudo/alias", sudoers: "User_Alias ADMINS = alice\nUser_Alias ADMINS = bob\nroot ALL=(ALL:ALL) ALL\n", code: 1,
			want: "2 sudoers-syntax blocker", text: `alias "ADMINS" is defined twice`},
		{name: "alias warnings", golden: "testdata/visudo/aliaswarn", sudoers: "root ALL=(ALL:ALL) ALL\nUser_Alias UNUSED = alice\nNOSUCH ALL=(ALL) ALL\n",
			want: "2 sudoers-syntax warning\n3 sudoers-syntax warning", text: `User_Alias "UNUSED" is defined but never used`},
		{name: "unterminated string", golden: "testdata/visudo/quote", sudoers: "root ALL=(ALL:ALL) ALL\nalice ALL=(ALL) \"unterminated\n", code: 1,
			want: "2 sudoers-syntax blocker", text: "a quoted string is not closed at column 30"},
		// Includes. As a user the real drop-ins cannot be opened: not a
		// clean run.
		{name: "includedir, user", golden: "testdata/visudo/incdir.user", sudoers: "root ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n",
			note: "visudo could not read everything; the included files were not all checked said: " +
				"visudo: /etc/sudoers.d/90-vboxuser-nopasswd: Permission denied|visudo: /etc/sudoers.d/README: Permission denied"},
		{name: "include of a missing file", golden: "testdata/visudo/incmissing", sudoers: "root ALL=(ALL:ALL) ALL\n@include /etc/sudoers.d/no-such-file\n", code: 1,
			want: "2 sudoers-syntax error", text: "includes a file that does not exist", raw: "visudo: /etc/sudoers.d/no-such-file: No such file or directory"},
		{name: "relative include, file exists", golden: "testdata/visudo/increl", sudoers: "root ALL=(ALL:ALL) ALL\n@include sudoers.local\n", have: []string{"/etc/sudoers.local"}, code: 1,
			note: "/etc/sudoers.local, included at line 2, was not checked: visudo looks for a relative include next to the copy it reads"},
		{name: "relative include, no file", golden: "testdata/visudo/increl", sudoers: "root ALL=(ALL:ALL) ALL\n#include sudoers.local\n", code: 1,
			want: "2 sudoers-syntax error", raw: "visudo: /etc/sudoers.local: No such file or directory"},
		{name: "error in a drop-in and in this file", golden: "testdata/visudo/incother.root", sudoers: "root ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\nalice ALL = = ALL\n", code: 1,
			want: "3 sudoers-syntax blocker", note: noteOther, text: "syntax error at column 13"},
		{name: "error in a drop-in only", golden: "testdata/visudo/incotheronly.root", sudoers: "root ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n", code: 1, note: noteOther},
		{name: "include of a device", golden: "testdata/visudo/incdevnull", sudoers: "root ALL=(ALL:ALL) ALL\n@include /dev/null\n", code: 1,
			note: "visudo could not read everything; the included files were not all checked said: visudo: /dev/null is not a regular file"},
		// A drop-in is checked alone, under its own name.
		{name: "drop-in", golden: "testdata/visudo/dropin", path: "/etc/sudoers.d/90-local", sudoers: "alice ALL=(ALL) NOPASSWD ALL\n", code: 1,
			want: "1 sudoers-syntax blocker", raw: "/etc/sudoers.d/90-local:1:26: syntax error\nalice ALL=(ALL) NOPASSWD ALL\n                         ^~~"},
		// A visudo that failed without a word, or with words sc does not
		// know, is not a clean run.
		{name: "exit 1, nothing said", golden: write("silent", "", ""), sudoers: sudoersOne, code: 1,
			note: "visudo exited 1 without a message sc understands; the file was not checked"},
		{name: "a message sc does not know", golden: write("unknown", "", "@DIR@/sudoers:2:5: something visudo learns to say later\n"), sudoers: sudoersOne, code: 1,
			want: "2 sudoers-syntax blocker", text: "visudo rejects the line at column 5"},
		{name: "a warning sc does not know", golden: write("unknownwarn", "@DIR@/sudoers: parsed OK\n", "@DIR@/sudoers:2:5: something visudo learns to say later\n"), sudoers: sudoersOne,
			want: "2 sudoers-syntax warning", text: "visudo warns about the line at column 5"},
	} {
		path := tc.path
		if path == "" {
			path = "/etc/sudoers"
		}
		c, args := fakeVisudo(t, tc.have, tc.golden, tc.code)
		rep, err := c.Check(context.Background(), path, []byte(tc.sudoers))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || notesSaid(rep) != tc.note {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		if len(rep.Findings) > 0 {
			if f := rep.Findings[0]; (tc.text != "" && f.Text != tc.text) || (tc.raw != "" && f.Raw != tc.raw) {
				t.Errorf("%s: first finding text %q raw %q", tc.name, f.Text, f.Raw)
			}
		}
		lines := strings.Split(tc.sudoers, "\n")
		for _, f := range rep.Findings {
			// Our sentence never repeats the file's line; the validator's
			// lines name the real file.
			if f.Path != path || f.Line > 0 && strings.Contains(f.Text, strings.TrimSpace(lines[f.Line-1])) ||
				strings.Contains(f.Text, c.Home) || strings.Contains(f.Raw, c.Home) || strings.Contains(f.Text, "abc") {
				t.Errorf("%s: finding %+v", tc.name, f)
			}
			if _, ok := Lookup(f.Rule); !ok {
				t.Errorf("rule %s is not in the table", f.Rule)
			}
		}
		for _, n := range rep.Notes {
			if strings.Contains(n, c.Home) || strings.Contains(n, "ALL") {
				t.Errorf("%s: note %q", tc.name, n)
			}
		}
		// visudo was given exactly the check-only form, on a scratch copy
		// named like the file that is gone afterwards.
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 3 || a[0] != "-c" || a[1] != "-f" || filepath.Base(a[2]) != filepath.Base(path) ||
			!strings.HasPrefix(a[2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: visudo arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
	}
}

// Two syntax errors at the same column are two findings to Added, and a
// broken line that only moved is not a new one.
func TestSudoersKeys(t *testing.T) {
	dir := t.TempDir()
	run := func(line int, sudoers string) []Finding {
		os.WriteFile(filepath.Join(dir, "g.out"), nil, 0o644)
		os.WriteFile(filepath.Join(dir, "g.err"), []byte(fmt.Sprintf("@DIR@/sudoers:%d:19: syntax error\n", line)), 0o644)
		c, _ := fakeVisudo(t, nil, filepath.Join(dir, "g"), 1)
		rep, err := c.Check(context.Background(), "/etc/sudoers", []byte(sudoers))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	before := run(2, "Defaults env_reset\nvboxuser ALL=(ALL NOPASSWD ALL\n")
	if before[0].Key == "" {
		t.Fatal("no key")
	}
	if got := brief(Added(before, run(3, "Defaults env_reset\n# moved\nvboxuser ALL=(ALL NOPASSWD ALL\n"))); got != "" {
		t.Errorf("moved: %q", got)
	}
	if got := brief(Added(before, run(2, "Defaults env_reset\nalice    ALL=(ALL NOPASSWD ALL\n"))); got != "2 sudoers-syntax blocker" {
		t.Errorf("another line: %q", got)
	}
}

// fakeInfo is what a faked lstat returns.
type fakeInfo struct {
	mode os.FileMode
	sys  any
}

func (f fakeInfo) Name() string       { return "sudoers" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return f.sys }

// The mode rule, on a machine without visudo, from a faked lstat of the
// real path.
func TestSudoersMode(t *testing.T) {
	stat := func(mode uint32, uid int, gid ...int) *syscall.Stat_t {
		st := &syscall.Stat_t{Mode: syscall.S_IFREG | mode, Uid: uint32(uid)}
		if len(gid) > 0 {
			st.Gid = uint32(gid[0])
		}
		return st
	}
	for _, tc := range []struct {
		name string
		fi   os.FileInfo
		err  error
		want string // the finding's text
		sev  Severity
		note string
	}{
		{name: "0440 root", fi: fakeInfo{0o440, stat(0o440, 0)}},
		// sudo reads these; visudo -c complains.
		{name: "0644 root", fi: fakeInfo{0o644, stat(0o644, 0)}, want: "the file is mode 0644 and owned by uid 0, not 0440 root", sev: Warning},
		{name: "setgid", fi: fakeInfo{0o440 | os.ModeSetgid, stat(0o2440, 0)}, want: "the file is mode 2440 and owned by uid 0, not 0440 root", sev: Warning},
		{name: "0660 root:root", fi: fakeInfo{0o660, stat(0o660, 0, 0)}, want: "the file is mode 0660 and owned by uid 0, not 0440 root", sev: Warning},
		// sudo ignores these.
		{name: "0440 not root", fi: fakeInfo{0o440, stat(0o440, 1000)}, want: "the file is mode 0440 and owned by uid 1000, not 0440 root", sev: Error},
		{name: "world-writable", fi: fakeInfo{0o446, stat(0o446, 0)}, want: "the file is mode 0446 and owned by uid 0, not 0440 root", sev: Error},
		{name: "group-writable, group not root", fi: fakeInfo{0o460, stat(0o460, 0, 27)}, want: "the file is mode 0460 and owned by uid 0, not 0440 root", sev: Error},
		{name: "0440, no owner known", fi: fakeInfo{0o440, nil}},
		{name: "0600, no owner known", fi: fakeInfo{0o600, nil}, want: "the file is mode 0600, not 0440", sev: Warning},
		{name: "a symlink", fi: fakeInfo{os.ModeSymlink | 0o777, nil}},
		{name: "a new file", err: fs.ErrNotExist},
		{name: "unreadable", err: &os.PathError{Op: "lstat", Path: "/etc/sudoers.d/90-local", Err: syscall.EACCES},
			note: "could not read the mode of /etc/sudoers.d/90-local (lstat /etc/sudoers.d/90-local: permission denied); sudoers-mode was not checked"},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		asked := ""
		c.lstat = func(p string) (os.FileInfo, error) { asked = p; return tc.fi, tc.err }
		rep, err := c.Check(context.Background(), "/etc/sudoers.d/90-local", []byte("alice ALL=(ALL) ALL\n"))
		if err != nil {
			t.Fatal(err)
		}
		if asked != "/etc/sudoers.d/90-local" {
			t.Errorf("%s: lstat of %q", tc.name, asked)
		}
		want, notes := "", "no validator found (visudo); only sc's own rules ran"
		if tc.want != "" {
			want = "0 sudoers-mode " + tc.sev.String()
		}
		if tc.note != "" {
			notes = tc.note + "|" + notes
		}
		if got := brief(rep.Findings); got != want || notesSaid(rep) != notes {
			t.Errorf("%s: %q %q", tc.name, got, rep.Notes)
		}
		if tc.want != "" && (rep.Findings[0].Text != tc.want || rep.Findings[0].Path != "/etc/sudoers.d/90-local") {
			t.Errorf("%s: %+v", tc.name, rep.Findings[0])
		}
	}
	// A bad mode on top of a syntax error: both.
	c, _ := fakeVisudo(t, nil, "testdata/visudo/one", 1)
	c.lstat = func(string) (os.FileInfo, error) { return fakeInfo{0o644, stat(0o644, 1000)}, nil }
	rep, err := c.Check(context.Background(), "/etc/sudoers", []byte(sudoersOne))
	if err != nil || brief(rep.Findings) != "0 sudoers-mode blocker\n2 sudoers-syntax blocker" || len(rep.Notes) != 0 {
		t.Errorf("both: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

// A visudo that was killed, or hangs, costs a note, not the check; the
// mode rule still runs.
func TestSudoersVisudoBroken(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	c.lstat = func(string) (os.FileInfo, error) {
		return fakeInfo{0o646, &syscall.Stat_t{Mode: syscall.S_IFREG | 0o646}}, nil // world-writable
	}
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "visudo"), []byte("#!/bin/sh\necho '/x/sudoers:2:19: syntax error' >&2\nkill -9 $$\n"), 0o755)
	rep, err := c.Check(context.Background(), "/etc/sudoers", []byte(sudoersOne))
	if err != nil || brief(rep.Findings) != "0 sudoers-mode blocker" || notesSaid(rep) != "visudo was killed; only sc's own rules ran" {
		t.Errorf("killed: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "visudo"), []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err = c.Check(context.Background(), "/etc/sudoers", []byte(sudoersOne))
	if err != nil || brief(rep.Findings) != "0 sudoers-mode blocker" || notesSaid(rep) != "visudo did not finish in time; only sc's own rules ran" {
		t.Errorf("hung: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	var zero fakeInfo
	if zero.IsDir() || zero.Size() != 0 || zero.Name() != "sudoers" || !zero.ModTime().IsZero() {
		t.Error("fakeInfo")
	}
	if _, _, note := (&Checks{lstat: func(string) (os.FileInfo, error) { return nil, errors.New("odd") }}).sudoersMode("/etc/sudoers"); note == "" {
		t.Error("an lstat error other than not-exist gives a note")
	}
}

// The real visudo, where installed, on fabricated content: a syntax
// error is found with its line and column, a good file is clean. The
// content includes nothing, so nothing under /etc is read.
func TestSudoersRealVisudo(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/visudo"); err != nil {
		t.Skip("no visudo")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome"),
		lstat: func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }}
	rep, err := c.Check(context.Background(), "/etc/sudoers", []byte("root ALL=(ALL:ALL) ALL\nvboxuser ALL=(ALL NOPASSWD ALL\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := brief(rep.Findings); got != "2 sudoers-syntax blocker" || len(rep.Notes) != 0 {
		t.Fatalf("findings:\n%s\nnotes %q", got, rep.Notes)
	}
	if f := rep.Findings[0]; f.Text != "syntax error at column 19" || !strings.HasPrefix(f.Raw, "/etc/sudoers:2:19: syntax error\n") {
		t.Errorf("finding %+v", f)
	}
	rep, err = c.Check(context.Background(), "/etc/sudoers.d/90-local", []byte("alice ALL=(ALL:ALL) NOPASSWD: ALL\n"))
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 0 {
		t.Errorf("good drop-in: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

// sudo skips a drop-in whose name has a dot or ends in ~ (sudoers(5)): one
// warning, whatever is in it, and visudo is not asked.
func TestSudoersIgnoredNames(t *testing.T) {
	for _, p := range []string{"/etc/sudoers.d/90-local.conf", "/etc/sudoers.d/90-local~", "/etc/sudoers.d/90-local.dpkg-old"} {
		c, args := fakeMachine(t, nil, "visudo", filepath.Join(t.TempDir(), "unused"), 0)
		rep, err := c.Check(context.Background(), p, []byte(sudoersOne))
		if err != nil || brief(rep.Findings) != "0 sudoers-ignored warning" || len(rep.Notes) != 0 {
			t.Errorf("%s: %q %q %v", p, brief(rep.Findings), rep.Notes, err)
		}
		if _, err := os.Stat(args); err == nil {
			t.Errorf("%s: visudo ran", p)
		}
	}
	// A name with neither, and /etc/sudoers itself, are read.
	c, _ := fakeMachine(t, nil, "", "", 0)
	c.lstat = func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
	for _, p := range []string{"/etc/sudoers.d/90-local", "/etc/sudoers"} {
		if rep, _ := c.Check(context.Background(), p, []byte(sudoersOne)); len(rep.Findings) != 0 {
			t.Errorf("%s: %q", p, brief(rep.Findings))
		}
	}
}

// A saved version is checked for what is in it: the mode of the file on
// disk today is not that version's.
func TestSudoersSavedVersion(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	asked := false
	c.lstat = func(string) (os.FileInfo, error) {
		asked = true
		return fakeInfo{0o646, &syscall.Stat_t{Mode: syscall.S_IFREG | 0o646}}, nil
	}
	rep, err := c.CheckSaved(context.Background(), "/etc/sudoers.d/90-local", []byte("alice ALL=(ALL) ALL\n"))
	if err != nil || len(rep.Findings) != 0 || asked {
		t.Fatalf("saved: %q, lstat asked %v, %v", brief(rep.Findings), asked, err)
	}
	if rep, _ := c.Check(context.Background(), "/etc/sudoers.d/90-local", []byte("alice ALL=(ALL) ALL\n")); brief(rep.Findings) != "0 sudoers-mode error" {
		t.Fatalf("on disk: %q", brief(rep.Findings))
	}
}

// /etc/sudoers with no rule and no include lets nobody use sudo (checked
// with sudo in a private mount namespace: "not allowed"). A drop-in may
// hold anything.
func TestSudoersNoRules(t *testing.T) {
	for _, tc := range []struct{ path, data, want string }{
		{"/etc/sudoers", "", "0 sudoers-no-rules blocker"},
		{"/etc/sudoers", "# all gone\n\nDefaults env_reset\nDefaults:alice !lecture\nUser_Alias\tADMINS = alice\n", "0 sudoers-no-rules blocker"},
		{"/etc/sudoers", "Defaults env_reset\n@includedir /etc/sudoers.d\n", ""},
		{"/etc/sudoers", "#includedir /etc/sudoers.d\n", ""},
		{"/etc/sudoers", "#include /etc/sudoers.local\n", ""},
		{"/etc/sudoers", "%sudo ALL=(ALL:ALL) ALL\n", ""},
		{"/etc/sudoers", "Defaults \\\n  env_reset\nroot\tALL=(ALL:ALL) ALL\r\n", ""},
		{"/etc/sudoers.d/90-local", "", ""},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		c.lstat = func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
		rep, err := c.Check(context.Background(), tc.path, []byte(tc.data))
		if err != nil || brief(rep.Findings) != tc.want {
			t.Errorf("%s %q: %q %v", tc.path, tc.data, brief(rep.Findings), err)
		}
	}
}

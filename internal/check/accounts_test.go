//go:build linux

package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pwRoot = "root:x:0:0:root:/root:/bin/bash\n"

// accountCase is a fabricated passwd or group file, the exit code of the
// real tool on it (its golden pair is testdata/TOOL/NAME), and what sc
// makes of it: brief() and the first finding's text.
type accountCase struct {
	name, data string
	code       int
	want, text string
}

// pwck -r's output, as captured on the dev VM (shadow 4.13) from these
// files and the shadow file sc makes up for each, stream by stream.
var pwckCases = []accountCase{
	{name: "good", data: pwRoot + "alice:x:1001:0:Alice:/tmp:/bin/sh\nbob:x:1002:0:Bob:/:/bin/sh\n"},
	// glibc reads these, with an empty shell or one with ":extra" in it.
	{name: "fields", data: pwRoot + "alice:x:1001:0:Alice:/tmp\nbob:x:1002:0:Bob:/tmp:/bin/sh:extra\n", code: 2,
		want: "2 passwd-invalid error\n3 passwd-invalid error", text: "the line does not have the 7 fields of a passwd entry: glibc reads it, but not as written"},
	// glibc skips all of these: the junk and the bad ids lose their user,
	// the blank and the comment nothing.
	{name: "unreadable", data: pwRoot + "junk line with hunter2\n\n# a comment\ncarol:x:abc:0:Carol:/tmp:/bin/sh\ndave:x:-1:0:Dave:/tmp:/bin/sh\n", code: 2,
		want: "2 passwd-invalid error\n3 passwd-invalid warning\n4 passwd-invalid warning\n5 passwd-invalid error\n6 passwd-invalid error",
		text: "glibc cannot read the line, so its user does not exist"},
	{name: "badid", data: pwRoot + "erin:x:4294967295:0:Erin:/tmp:/bin/sh\n", code: 2,
		want: "2 passwd-invalid error", text: "uid 4294967295 means no uid to the kernel, so this user cannot log in or run anything"},
	// pwck names every line of a name that is on two; only the later ones
	// are ignored by glibc. Two equal lines are two lines.
	{name: "dup", data: pwRoot + "alice:x:1001:0:Alice:/tmp:/bin/sh\nbob:x:1002:0:Bob:/tmp:/bin/sh\nalice:x:1003:0:Alice again:/tmp:/bin/sh\nbob:x:1002:0:Bob:/tmp:/bin/sh\n", code: 2,
		want: "4 passwd-invalid warning\n5 passwd-invalid warning", text: "user alice is defined on an earlier line too: lookups by name use that one, not this one"},
	{name: "names", data: pwRoot + "1234:x:1001:0:Digits:/tmp:/bin/sh\n  alice:x:1002:0:Alice:/tmp:/bin/sh\n", code: 2,
		want: "2 passwd-invalid warning\n3 passwd-invalid warning", text: "pwck does not accept the user name on this line: glibc reads it, but other tools may refuse it"},
	// What pwck checks on this machine: a missing group is a warning, a
	// missing home nothing, a missing shell an error unless it is nologin.
	{name: "machine", data: pwRoot + "erin:x:1001:4242:Erin:/tmp:/bin/sh\nfrank:x:1002:0:Frank:/sc-no-such-home:/bin/sh\n" +
		"grace:x:1003:0:Grace:/tmp:/bin/sc-no-such-shell\nsc-daemon:x:998:0::/tmp:/usr/local/sbin/nologin\n", code: 2,
		want: "2 passwd-invalid warning\n4 passwd-shell-missing error\n5 passwd-shell-missing warning", text: "the primary group 4242 of user erin does not exist on this machine"},
	{name: "crlf", data: "root:x:0:0:root:/root:/bin/bash\r\nalice:x:1001:0:Alice:/tmp:/bin/sh\r\n", code: 2,
		want: "1 passwd-shell-missing error\n2 passwd-shell-missing error",
		text: `the shell "/bin/bash\r" of user root does not exist on this machine, so root cannot log in (it ends in a carriage return: a DOS line ending)`},
	{name: "notx", data: pwRoot + "heidi:sc-not-a-hash:1001:0:Heidi:/tmp:/bin/sh\nivan::1002:0:Ivan:/tmp:/bin/sh\njudy:!:1003:0:Judy:/tmp:/bin/sh\n", code: 2,
		want: "2 passwd-invalid warning\n3 passwd-invalid warning\n4 passwd-invalid warning", text: "the password field of user heidi is not x, so /etc/shadow is not used for it"},
	// A broken root line is one blocker, passwd-root's, and pwck's words
	// about the made-up shadow file are left out.
	{name: "rootbroken", data: "root:x:0:zero:root:/root:/bin/bash\nalice:x:1001:0:Alice:/tmp:/bin/sh\n", code: 2,
		want: "1 passwd-root blocker", text: "glibc cannot read root's line, so root does not exist: sudo and su fail"},
	{name: "rootuid", data: "root:x:1000:0:root:/root:/bin/bash\nalice:x:1001:0:Alice:/tmp:/bin/sh\n",
		want: "1 passwd-root blocker", text: "the first line for root gives it uid 1000, not 0: sudo and su would run as uid 1000"},
	{name: "nis", data: pwRoot + "+alice\n-bob\n+@admins::::::\n"},
}

// grpck -r -S's output, captured as pwck's.
var grpckCases = []accountCase{
	{name: "good", data: "root:x:0:\nsudo:x:27:root\nscgrp:x:1001:\n"},
	{name: "unreadable", data: "root:x:0:\njunk line with hunter2\n\n# a comment\nscgrp:x:abc:\nsudo:x:twentyseven:root\n", code: 2,
		want: "2 group-invalid error\n3 group-invalid warning\n4 group-invalid warning\n5 group-invalid error\n6 group-invalid blocker",
		text: "glibc cannot read the line, so its group does not exist: its members lose it at their next login"},
	// grpck misreads the three-field line as having a member "2": glibc
	// sees none, so that is no finding.
	{name: "fields", data: "root:x:0:\nsudo:x:27:root\nscgrp:x:1001::extra\nscother:x:1002\n", code: 2,
		want: "3 group-invalid error", text: "the line does not have the 4 fields of a group entry: glibc reads it, but not as written"},
	{name: "dup", data: "root:x:0:\nscgrp:x:1001:\nscgrp:x:1002:\n", code: 2,
		want: "3 group-invalid warning", text: "group scgrp is defined on an earlier line too: lookups by name use that one, not this one"},
	{name: "names", data: "root:x:0:\nsc grp:x:1001:\nscgrp:x:4294967295:\n", code: 2,
		want: "2 group-invalid warning\n3 group-invalid error", text: "grpck does not accept the group name on this line: glibc reads it, but other tools may refuse it"},
	// glibc skips an empty member and blanks before one.
	{name: "members", data: "root:x:0:\nsudo:x:27:root,scnouser\nscgrp:x:1001:root,,root\nscsp:x:1002: root\n", code: 2,
		want: "2 group-invalid error", text: "member scnouser of group sudo is not a user on this machine, so whoever was meant has no sudo"},
	{name: "crlf", data: "root:x:0:\r\nsudo:x:27:root\r\n", code: 2,
		want: "2 group-invalid error", text: `member "root\r" of group sudo is not a user on this machine, so whoever was meant has no sudo (it ends in a carriage return: a DOS line ending)`},
	{name: "notx", data: "root:x:0:\nsudo:sc-not-a-hash:27:root\n", code: 2,
		want: "2 group-invalid warning", text: "the password field of group sudo is not x, so /etc/gshadow is not used for it"},
	{name: "nis", data: "root:x:0:\n+scgrp\n-other\n"},
}

// runAccounts checks each case with a fake tool that prints its golden
// pair, and checks what every finding may and may not carry.
func runAccounts(t *testing.T, tool, path string, cases []accountCase) {
	t.Helper()
	for _, tc := range cases {
		c, _ := fakeMachine(t, nil, "", "", 0)
		args := goldenTool(t, c, tool, "testdata/"+tool+"/"+tc.name, tc.code)
		rep, err := c.Check(context.Background(), path, []byte(tc.data))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || len(rep.Notes) != 0 {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q", tc.name, got, tc.want, rep.Notes)
		}
		if tc.text != "" && (len(rep.Findings) == 0 || rep.Findings[0].Text != tc.text) {
			t.Errorf("%s: text %+v, want %q", tc.name, rep.Findings, tc.text)
		}
		lines := strings.Split(tc.data, "\n")
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok || f.Path != path || f.Line > 0 && f.Key == "" {
				t.Errorf("%s: finding %+v", tc.name, f)
			}
			// Neither our sentence nor the tool's lines quote the file's
			// line: the tools' questions do, and they are dropped.
			for _, s := range []string{f.Text, f.Raw} {
				if strings.Contains(s, "hunter2") || strings.Contains(s, "sc-not-a-hash") || strings.Contains(s, c.Home) || strings.Contains(s, "? No") ||
					f.Line > 0 && len(strings.TrimSpace(lines[f.Line-1])) > 3 && strings.Contains(s, strings.TrimSpace(lines[f.Line-1])) {
					t.Errorf("%s: line %d: %q quotes the file or the scratch copy", tc.name, f.Line, s)
				}
			}
		}
		// The tool read the scratch copy and the made-up file next to it,
		// read-only; both are gone afterwards.
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		shadow := map[string]string{"pwck": "shadow", "grpck": "gshadow"}[tool]
		if len(a) < 3 || a[0] != "-r" || filepath.Base(a[len(a)-2]) != filepath.Base(path) || filepath.Base(a[len(a)-1]) != shadow ||
			filepath.Dir(a[len(a)-1]) != filepath.Dir(a[len(a)-2]) || !strings.HasPrefix(a[len(a)-2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
	}
}

func TestPasswdPwck(t *testing.T) {
	runAccounts(t, "pwck", "/etc/passwd", pwckCases)
	// The raw line names the real files, never the scratch copy.
	c, _ := fakeMachine(t, nil, "", "", 0)
	goldenTool(t, c, "pwck", "testdata/pwck/notx", 2)
	rep, _ := c.Check(context.Background(), "/etc/passwd", []byte(pwRoot+"heidi:sc-not-a-hash:1001:0:Heidi:/tmp:/bin/sh\nivan::1002:0:Ivan:/tmp:/bin/sh\njudy:!:1003:0:Judy:/tmp:/bin/sh\n"))
	if len(rep.Findings) != 3 || rep.Findings[0].Raw != "user heidi has an entry in /etc/shadow, but its password field in /etc/passwd is not set to 'x'" ||
		rep.Findings[1].Text != "the password field of user ivan is empty, not x: Ubuntu's PAM (nullok) lets it log in without a password" {
		t.Errorf("notx: %+v", rep.Findings)
	}
}

func TestGroupGrpck(t *testing.T) {
	runAccounts(t, "grpck", "/etc/group", grpckCases)
	c, _ := fakeMachine(t, nil, "", "", 0)
	args := goldenTool(t, c, "grpck", "testdata/grpck/good", 0)
	c.Check(context.Background(), "/etc/group", []byte("root:x:0:\n"))
	if b, _ := os.ReadFile(args); !strings.HasPrefix(string(b), "-r\n-S\n") {
		t.Errorf("grpck arguments %q", b)
	}
}

// sc's own root rule, as glibc resolves the name root: the first line it
// can read with that name must have uid 0. No pwck here.
func TestPasswdRoot(t *testing.T) {
	const noPwck = "no validator found (pwck); only sc's own rules ran"
	runPlain(t, "/etc/passwd", nil, []plainCase{
		{name: "stock", data: pwRoot + "daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n", note: noPwck},
		{name: "empty file", want: "0 passwd-root blocker", note: noPwck},
		{name: "no root", data: "alice:x:1001:0:Alice:/tmp:/bin/sh\n", want: "0 passwd-root blocker", note: noPwck},
		{name: "a commented root", data: "#root:x:0:0:root:/root:/bin/bash\n", want: "0 passwd-root blocker", note: noPwck},
		{name: "a NIS root", data: "+root\n", want: "0 passwd-root blocker", note: noPwck},
		{name: "root's uid is 1", data: "root:x:1:0:root:/root:/bin/bash\n", want: "1 passwd-root blocker", note: noPwck},
		{name: "a second root with uid 0 comes too late", data: "alice:x:1:1::/:/bin/sh\nroot:x:1000:0::/root:/bin/sh\n" + pwRoot, want: "2 passwd-root blocker", note: noPwck},
		{name: "a second root after the right one", data: pwRoot + "root:x:1000:0::/root:/bin/sh\n", note: noPwck},
		{name: "root's line broken", data: "root:x:0:zero:root:/root:/bin/bash\n", want: "1 passwd-root blocker", note: noPwck},
		{name: "a broken root line, then a good one", data: "root:x:zero:0:root:/root:/bin/bash\n" + pwRoot, note: noPwck},
		{name: "three fields: no gid", data: "root:x:0\n", want: "1 passwd-root blocker", note: noPwck},
		// glibc reads these as root with uid 0.
		{name: "four fields", data: "root:x:0:0\n", note: noPwck},
		{name: "blanks before the line and the uid, a sign", data: "  root:x: +0:0:root:/root:/bin/bash\n", note: noPwck},
		{name: "another uid 0 user is fine", data: pwRoot + "toor:x:0:0::/root:/bin/sh\n", note: noPwck},
		{name: "no newline at the end", data: strings.TrimSuffix(pwRoot, "\n"), note: noPwck},
	})
	// With nss-systemd on the passwd line, a missing or unreadable root
	// line is a warning: root still resolves (checked on the dev VM). A
	// root line with another uid is still a blocker: glibc stops at it.
	c, _ := fakeMachine(t, nil, "", "", 0)
	for _, tc := range []struct{ nsswitch, data, want, text string }{
		{"passwd: files systemd\n", "alice:x:1001:0:Alice:/tmp:/bin/sh\n", "0 passwd-root warning",
			"no line defines root, but nss-systemd supplies it with uid 0"},
		{"passwd: files systemd\n", "root:x:0:zero:root:/root:/bin/bash\n", "1 passwd-root warning",
			"glibc cannot read root's line, but nss-systemd supplies root with uid 0"},
		{"passwd: files systemd\n", "root:x:1000:0:root:/root:/bin/bash\n", "1 passwd-root blocker", ""},
		{"passwd: files\n", "alice:x:1001:0:Alice:/tmp:/bin/sh\n", "0 passwd-root blocker", ""},
		{"passwd: files sss\ngroup: files systemd\n", "alice:x:1001:0:Alice:/tmp:/bin/sh\n", "0 passwd-root blocker", ""},
	} {
		c.nsswitchPath = filepath.Join(t.TempDir(), "nsswitch.conf")
		os.WriteFile(c.nsswitchPath, []byte(tc.nsswitch), 0o644)
		rep, err := c.Check(context.Background(), "/etc/passwd", []byte(tc.data))
		if err != nil || brief(rep.Findings) != tc.want || (tc.text != "" && rep.Findings[0].Text != tc.text) {
			t.Errorf("%q, %q: %q %v", tc.nsswitch, tc.data, brief(rep.Findings), err)
		}
	}
}

// glibcEntry and glibcID against what glibc 2.39's fgetpwent and fgetgrent
// returned for the same lines on the dev VM.
func TestGlibcEntry(t *testing.T) {
	for l, want := range map[string]bool{
		"alice:x:1001:0:Alice:/tmp:/bin/sh":         true,
		"alice:x:1001:0:Alice:/tmp":                 true, // shell ""
		"alice:x:1001:0:Alice:/tmp:/bin/bash:extra": true, // shell "/bin/bash:extra"
		"alice:x:1001:0":                            true,
		"alice:x:1001":                              false,
		"alice:x:abc:0:Alice:/tmp:/bin/sh":          false,
		"alice:x:1001:abc:Alice:/tmp:/bin/sh":       false,
		"alice:x::0:Alice:/tmp:/bin/sh":             false,
		"alice:x:4294967295:0:Alice:/tmp:/bin/sh":   true,
		"alice:x:4294967296:0:Alice:/tmp:/bin/sh":   false,
		"alice:x:-1:0:Alice:/tmp:/bin/sh":           false,
		"alice:x: 1006:0:F:/tmp:/bin/sh":            true,
		"alice:x:1007 :0:F:/tmp:/bin/sh":            false,
		"alice:x:+1008:0:F:/tmp:/bin/sh":            true,
		"alice:x:0x10:0:F:/tmp:/bin/sh":             false,
		"Al ice:x:1001:0:Alice:/tmp:/bin/bash":      true,
		":x:1001:0:Alice:/tmp:/bin/bash":            true,
		"  alice:x:1001:0:A:/tmp:/bin/sh":           true,
		"":                                          false,
		"# a comment":                               false,
		"junk line with hunter2":                    false,
		"+alice":                                    true,
		"-bob":                                      true,
		"+@grp::::::":                               true,
	} {
		if _, ok := glibcEntry(l, 7, 2, 3); ok != want {
			t.Errorf("passwd line %q: %v, want %v", l, ok, want)
		}
	}
	for l, want := range map[string]bool{
		"sudo:x:27:root":        true,
		"scgrp:x:1001":          true,
		"scgrp:x:1001::extra":   true,
		"scgrp:x:abc:":          false,
		"scgrp:x::":             false,
		"scgrp:x:4294967295:":   true,
		"sc grp:x:1001:":        true,
		"+scgrp":                true,
		"junk line hunter2":     false,
		"sudo:x:twentyseven:me": false,
	} {
		if _, ok := glibcEntry(l, 4, 2); ok != want {
			t.Errorf("group line %q: %v, want %v", l, ok, want)
		}
	}
	// Blanks before a member are dropped, blanks after it kept.
	for _, tc := range []struct {
		line, name string
		want       bool
	}{
		{"sudo:x:27: root , alice\t,bob ", "root ", true},
		{"sudo:x:27: root , alice\t,bob ", "alice\t", true},
		{"sudo:x:27: root , alice\t,bob ", "root", false},
		{"sudo:x:27: root", " root", false},
		{"sudo:x:27:root\r", "root\r", true},
		{"sudo:x:27:root,,root", "", false},
		{"root:x:0:\r", "\r", false},
		{"scother:x:1002", "2", false},
	} {
		if got := glibcMember(tc.line, tc.name); got != tc.want {
			t.Errorf("glibcMember(%q, %q) = %v", tc.line, tc.name, got)
		}
	}
}

// The shadow file sc makes up: one entry per name of a line with the
// right number of fields, no NIS line, no name twice, no password.
func TestAccountsCompanion(t *testing.T) {
	for _, tc := range []struct {
		tool, path, data, want string
	}{
		{"pwck", "/etc/passwd", pwRoot + "alice:x:1:1::/:/bin/sh\nalice:x:2:1::/:/bin/sh\njunk\nbob:x:3:1::/\n+carol::::::\n:x:4:1::/:/bin/sh\n",
			"root:*:::::::\nalice:*:::::::\n"},
		{"grpck", "/etc/group", "root:x:0:\nsudo:x:27:root\nsudo:x:28:\nshort:x:1\n-other:x:2:\n", "root:!::\nsudo:!::\n"},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		seen := filepath.Join(t.TempDir(), "seen")
		os.WriteFile(filepath.Join(c.Run.Dirs[0], tc.tool), []byte("#!/bin/sh\nfor a in \"$@\"; do f=$a; done\ncat \"$f\" > "+seen+"\n"), 0o755)
		if _, err := c.Check(context.Background(), tc.path, []byte(tc.data)); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(seen); string(b) != tc.want {
			t.Errorf("%s: made-up file %q, want %q", tc.tool, b, tc.want)
		}
	}
}

// A broken line that only moved is not new to Added; another broken line
// is, though its finding has the same text.
func TestAccountsKeys(t *testing.T) {
	run := func(golden, data string) []Finding {
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "pwck", golden, 2)
		rep, err := c.Check(context.Background(), "/etc/passwd", []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	junk := func(line string) string {
		return writeGolden(t, "junk", "invalid password file entry\ndelete line '"+line+"'? No\npwck: no changes\n", "")
	}
	before := run(junk("junk one"), pwRoot+"junk one\n")
	if got := brief(Added(before, run(junk("junk one"), "# moved\n"+pwRoot+"junk one\n"))); got != "" {
		t.Errorf("moved: %q", got)
	}
	if got := brief(Added(before, run(junk("junk two"), pwRoot+"junk two\n"))); got != "2 passwd-invalid error" {
		t.Errorf("another line: %q", got)
	}
}

// A pwck or grpck that did not check the file says so in a note, never
// looks like a clean run; passwd-root still runs.
func TestAccountsToolBroken(t *testing.T) {
	data := map[string]string{"pwck": pwRoot, "grpck": "root:x:0:\n"}
	path := map[string]string{"pwck": "/etc/passwd", "grpck": "/etc/group"}
	for _, tool := range []string{"pwck", "grpck"} {
		for _, tc := range []struct {
			name, script string
			want, note   string
		}{
			{"missing", "", "", "no validator found (" + tool + "); only sc's own rules ran"},
			{"killed", "echo 'invalid password file entry'\nkill -9 $$\n", "", tool + " was killed; only sc's own rules ran"},
			{"hung", "sleep 60\n", "", tool + " did not finish in time; only sc's own rules ran"},
			{"cannot open", "for a in \"$@\"; do f=$a; done\necho \"" + tool + ": cannot open $f\" >&2\nexit 3\n", "",
				tool + " could not check the file (" + tool + ": cannot open " + map[string]string{"pwck": "/etc/shadow", "grpck": "/etc/gshadow"}[tool] + ")"},
			{"usage", "exit 1\n", "", tool + " could not check the file (exit 1)"},
			{"exit 2, nothing said", "exit 2\n", "", tool + " exited 2 without a message sc understands; the file was not checked"},
			{"exit 2, only its last line", "echo '" + tool + ": no changes'\nexit 2\n", "", tool + " exited 2 without a message sc understands; the file was not checked"},
			{"something on stderr", "echo 'a new remark' >&2\nexit 0\n", "", tool + " printed something sc does not understand (a new remark)"},
			{"a message sc does not know", "echo 'a check pwck learns later'\necho \"delete line '" + strings.TrimSuffix(data[tool], "\n") + "'? No\"\nexit 2\n",
				"1 " + map[string]string{"pwck": "passwd-invalid", "grpck": "group-invalid"}[tool] + " warning", ""},
		} {
			c, _ := fakeMachine(t, nil, "", "", 0)
			c.Run.Timeout = 300 * time.Millisecond
			if tc.script != "" {
				os.WriteFile(filepath.Join(c.Run.Dirs[0], tool), []byte("#!/bin/sh\n"+tc.script), 0o755)
			}
			rep, err := c.Check(context.Background(), path[tool], []byte(data[tool]))
			if err != nil || brief(rep.Findings) != tc.want || strings.Join(rep.Notes, "|") != tc.note {
				t.Errorf("%s %s: %q %q %v", tool, tc.name, brief(rep.Findings), rep.Notes, err)
			}
			for _, f := range rep.Findings {
				if f.Text != tool+" reports a problem sc has no rule for" || f.Raw != "a check pwck learns later" || strings.Contains(f.Raw, "root:x") {
					t.Errorf("%s %s: %+v", tool, tc.name, f)
				}
			}
		}
	}
	// Without pwck, passwd-root still runs.
	c, _ := fakeMachine(t, nil, "", "", 0)
	rep, _ := c.Check(context.Background(), "/etc/passwd", []byte("alice:x:1:1::/:/bin/sh\n"))
	if brief(rep.Findings) != "0 passwd-root blocker" || len(rep.Notes) != 1 {
		t.Errorf("no pwck: %q %q", brief(rep.Findings), rep.Notes)
	}
}

// The real pwck and grpck, where installed, on fabricated files: a junk
// line, a duplicate, a missing shell and a member that is not a user are
// found; a home that does not exist is not; good files are clean. Nothing
// under /etc is read but what the tools read for themselves (login.defs,
// and the users and groups they look up).
func TestAccountsRealTools(t *testing.T) {
	for _, tc := range []struct {
		tool, path, bad, want, good string
	}{
		{"pwck", "/etc/passwd",
			pwRoot + "alice:x:1001:0:Alice:/tmp:/bin/sh\njunk line with hunter2\ncarol:x:1002:0:Carol:/sc-no-such-home:/bin/sc-no-such-shell\nalice:x:1003:0:Alice again:/tmp:/bin/sh\n",
			"3 passwd-invalid error\n4 passwd-shell-missing error\n5 passwd-invalid warning",
			pwRoot + "alice:x:1001:0:Alice:/tmp:/bin/sh\nbob:x:1002:0:Bob:/:/bin/sh\n"},
		{"grpck", "/etc/group",
			"root:x:0:\nsudo:x:27:root,scnouser\njunk line\nscgrp:x:1001:\nscgrp:x:1002:\n",
			"2 group-invalid error\n3 group-invalid error\n5 group-invalid warning",
			"root:x:0:\nsudo:x:27:root\nscgrp:x:1001:\n"},
	} {
		if _, err := os.Stat("/usr/sbin/" + tc.tool); err != nil {
			t.Logf("no %s", tc.tool)
			continue
		}
		// The admins of this machine are not looked for (TestAdmins).
		c := &Checks{Home: filepath.Join(t.TempDir(), "schome"), groupPath: "/sc-no-such-group"}
		rep, err := c.Check(context.Background(), tc.path, []byte(tc.bad))
		if err != nil || brief(rep.Findings) != tc.want || len(rep.Notes) != 0 {
			t.Errorf("%s: findings:\n%s\nnotes %q %v", tc.tool, brief(rep.Findings), rep.Notes, err)
		}
		rep, err = c.Check(context.Background(), tc.path, []byte(tc.good))
		if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 0 {
			t.Errorf("%s: good file: %q %q %v", tc.tool, brief(rep.Findings), rep.Notes, err)
		}
	}
}

// A member that is not a user is an error in sudo or admin (whoever was
// meant loses sudo at their next login), a warning in another group.
func TestGroupMemberSeverity(t *testing.T) {
	for _, tc := range []struct{ group, want string }{
		{"sudo", "2 group-invalid error"},
		{"admin", "2 group-invalid error"},
		{"scgrp", "2 group-invalid warning"},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		goldenTool(t, c, "grpck", writeGolden(t, "m", "group "+tc.group+": no user scnouser\n", ""), 2)
		rep, err := c.Check(context.Background(), "/etc/group", []byte("root:x:0:\n"+tc.group+":x:27:scnouser\n"))
		if err != nil || brief(rep.Findings) != tc.want {
			t.Errorf("%s: %q %v", tc.group, brief(rep.Findings), err)
		}
	}
}

// An account file that leaves no admin locks Ubuntu's owner out, as root
// has no password (checked with sudo in a private mount namespace: an
// empty or root-only passwd, or a group file without the sudo line, and
// uid 1000 can no longer sudo). The admins are the members of sudo and
// admin in the machine's group file; a group file's members must be
// users in the machine's passwd file.
func TestAdmins(t *testing.T) {
	dir := t.TempDir()
	group := filepath.Join(dir, "group")
	os.WriteFile(group, []byte("root:x:0:\nsudo:x:27:alice,bob\nadmin:x:116:\n"), 0o644)
	passwd := filepath.Join(dir, "passwd")
	os.WriteFile(passwd, []byte(pwRoot+"alice:x:1000:1000::/home/alice:/bin/bash\nbob:x:1001:1001::/home/bob:/bin/bash\n"), 0o644)
	for _, tc := range []struct{ path, data, want string }{
		{"/etc/passwd", pwRoot + "alice:x:1000:1000::/home/alice:/bin/bash\nbob:x:1001:1001::/home/bob:/bin/bash\n", ""},
		{"/etc/passwd", pwRoot + "alice:x:1000:1000::/home/alice:/bin/bash\n", "0 passwd-no-admin error"},
		{"/etc/passwd", pwRoot, "0 passwd-no-admin blocker"},
		{"/etc/passwd", "", "0 passwd-root blocker\n0 passwd-no-admin blocker"},
		{"/etc/group", "root:x:0:\nsudo:x:27:alice\n", ""},
		{"/etc/group", "root:x:0:\nadmin:x:116:bob\n", ""},
		{"/etc/group", "root:x:0:\nsudo:x:27:\n", "2 group-no-admin blocker"},
		{"/etc/group", "root:x:0:\nsudo:x:27:carol\n", "2 group-no-admin blocker"},
		{"/etc/group", "root:x:0:\n", "0 group-no-admin blocker"},
		{"/etc/group", "", "0 group-no-admin blocker"},
	} {
		c, _ := fakeMachine(t, nil, "", "", 0)
		c.groupPath, c.passwdPath = group, passwd
		rep, err := c.Check(context.Background(), tc.path, []byte(tc.data))
		if err != nil || brief(rep.Findings) != tc.want {
			t.Errorf("%s %q: %q %v", tc.path, tc.data, brief(rep.Findings), err)
		}
	}
	// Without the machine's files there is nothing to compare with.
	c, _ := fakeMachine(t, nil, "", "", 0)
	if rep, _ := c.Check(context.Background(), "/etc/group", nil); brief(rep.Findings) != "" {
		t.Errorf("no passwd: %q", brief(rep.Findings))
	}
	c.groupPath, c.passwdPath = group, passwd
	rep, _ := c.Check(context.Background(), "/etc/passwd", []byte(pwRoot))
	if rep.Findings[0].Text != "no line is left for alice, bob (in sudo or admin): nobody can log in and use sudo" {
		t.Errorf("text %q", rep.Findings[0].Text)
	}
}

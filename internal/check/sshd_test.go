//go:build linux

package check

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sshdMachine is a fakeMachine whose sshd prints golden.out and golden.err
// and exits with code. sshd names the file it reads in its messages, so
// the golden files (captured from the real sshd on fabricated files in a
// scratch directory) say @FILE@ for it, which the fake fills in from -f.
func sshdMachine(t *testing.T, have []string, golden string, code int) (*Checks, string) {
	t.Helper()
	c, args := fakeMachine(t, have, "", "", 0)
	golden, _ = filepath.Abs(golden)
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > %s\nsed \"s|@FILE@|$3|g\" %s.out\nsed \"s|@FILE@|$3|g\" %s.err >&2\nexit %d\n", args, golden, golden, code)
	if err := os.WriteFile(filepath.Join(c.Run.Dirs[0], "sshd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return c, args
}

const (
	sshdConfig  = "/etc/ssh/sshd_config"
	sshdDropIn  = "/etc/ssh/sshd_config.d/50-local.conf"
	noteNoKeys  = "sshd found no host key it may read (the machine's are root's); the file was not checked to the end"
	badLines    = "Port 22\nPermitRootLogn no\nPermitRootLogin maybe\nPort abc\n"
	noteInclude = "sshd reports a problem in another file: /etc/ssh/sshd_config.d/50-bad.conf: line 1: Bad configuration option: PermitRootLogn"
)

// sshd's output, as captured on the dev VM (OpenSSH 9.6p1) from fabricated
// files with -h and a throwaway key, or without a key where the name says
// nokey. The paths of included files are rewritten to real-looking ones.
func TestSshdGolden(t *testing.T) {
	keyFile := "/etc/ssh/ssh_host_ed25519_key"
	for _, tc := range []struct {
		name, golden string
		code         int
		path, data   string
		want, note   string
	}{
		{"good", "good.user", 0, sshdConfig, "Port 22\nPermitRootLogin no\n", "", ""},
		{"unknown keyword", "unknown.user", 255, sshdConfig, "Port 22\nPermitRootLogn no\n", "2 sshd-invalid blocker", ""},
		{"bad value", "badvalue.user", 255, sshdConfig, "Port 22\nPermitRootLogin maybe\n", "2 sshd-invalid blocker", ""},
		// A bad value is fatal: the lines after it are not read.
		{"two bad lines", "two.user", 255, sshdConfig, badLines, "2 sshd-invalid blocker\n3 sshd-invalid blocker", ""},
		{"two unknown keywords", "twounknown.user", 255, sshdConfig, "PermitRootLogn no\nFooBar x\nPort 22\n", "1 sshd-invalid blocker\n2 sshd-invalid blocker", ""},
		{"a Match block", "match.user", 255, sshdConfig, "Port 22\nMatch User bob\n  Port 2222\n", "3 sshd-invalid blocker", ""},
		{"a Match condition", "matchcond.user", 255, sshdConfig, "Port 22\nMatch Usr bob\n  X11Forwarding no\n", "2 sshd-invalid blocker", ""},
		{"an unknown keyword in a Match block", "matchunknown.user", 255, sshdConfig, "Match User bob\n  PermitRootLogn no\n", "2 sshd-invalid blocker", ""},
		// Deprecated options are notices: exit 0, and not findings next
		// to an error either.
		{"deprecated options", "deprecated.user", 0, sshdConfig, "Port 22\nRSAAuthentication yes\nUsePrivilegeSeparation yes\nKeyRegenerationInterval 5\n", "", ""},
		{"deprecated and bad", "depandbad.user", 255, sshdConfig, "Port 22\nRSAAuthentication yes\nPermitRootLogn no\n", "3 sshd-invalid blocker", ""},
		// Another file's problems are notes.
		{"an error in an included file", "include.user", 255, sshdConfig, "Port 22\nInclude /etc/ssh/sshd_config.d/*.conf\nPermitRootLogin no\n", "", noteInclude},
		{"errors here and in an included file", "mainandinclude.user", 255, sshdConfig, "FooBar x\nInclude /etc/ssh/sshd_config.d/*.conf\n", "1 sshd-invalid blocker", noteInclude},
		{"a fatal error in an included file", "includefatal.user", 255, sshdConfig, "Port 22\nInclude /etc/ssh/sshd_config.d/*.conf\n", "",
			"sshd reports a problem in another file: /etc/ssh/sshd_config.d/50-fatal.conf line 1: unsupported option \"maybe\"."},
		{"a drop-in checked alone", "good.user", 0, sshdDropIn, "PasswordAuthentication no\n", "", ""},
		{"a drop-in with a bad value", "badvalue.user", 255, sshdDropIn, "Port 22\nPermitRootLogin maybe\n", "2 sshd-invalid blocker", ""},
		// Refused without a line number.
		{"a setting without its partner", "akc.user", 255, sshdConfig, "AuthorizedKeysCommand /usr/bin/true\n", "0 sshd-invalid blocker", ""},
		{"an address that does not resolve", "listen.user", 255, sshdConfig, "ListenAddress 999.1.1.1\n", "0 sshd-invalid blocker", ""},
		// The machine, not the file.
		{"no host keys (not root)", "nokey.user", 1, sshdConfig, "Port 22\n", "", noteNoKeys},
		{"a HostKey that is unreadable (not root)", "nokey.keyboth.user", 1, sshdConfig, "HostKey " + keyFile + "\nHostKey /nonexistent/key\n", "", noteNoKeys},
		{"a HostKey that exists elsewhere", "nokey.keymissing.user", 1, sshdDropIn, "Port 22\n", "", "sshd reports a problem in another file: host key /nonexistent/key does not exist"},
		{"a HostKey that does not exist, with others", "keymissing.user", 0, sshdConfig, "HostKey /nonexistent/key\n", "", ""},
		{"the file could not be read", "nofile.user", 1, sshdConfig, "Port 22\n", "", "sshd could not check the file (No such file or directory)"},
	} {
		c, args := sshdMachine(t, []string{keyFile}, "testdata/sshd/"+tc.golden, tc.code)
		rep, err := c.Check(context.Background(), tc.path, []byte(tc.data))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
			continue
		}
		// sshd was given exactly the check-only form, on a scratch copy
		// named like the file and gone afterwards, with no key: this
		// machine's are the ones that count.
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 3 || a[0] != "-t" || a[1] != "-f" || filepath.Base(a[2]) != filepath.Base(tc.path) ||
			!strings.HasPrefix(a[2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: sshd arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
		for _, f := range rep.Findings {
			if f.Path != tc.path || strings.Contains(f.Raw, c.Home) || strings.Contains(f.Text, "maybe") {
				t.Errorf("%s: finding %+v", tc.name, f)
			}
			if _, ok := Lookup(f.Rule); !ok {
				t.Errorf("rule %s is not in the table", f.Rule)
			}
		}
		for _, n := range rep.Notes {
			if strings.Contains(n, c.Home) {
				t.Errorf("%s: note %q", tc.name, n)
			}
		}
		text := func(i int) string { return rep.Findings[i].Text + " | " + rep.Findings[i].Raw }
		switch tc.name {
		case "unknown keyword":
			if got := text(0); got != `unknown option "PermitRootLogn" | /etc/ssh/sshd_config: line 2: Bad configuration option: PermitRootLogn` {
				t.Errorf("%s: %s", tc.name, got)
			}
		case "bad value":
			if got := text(0); got != `sshd does not accept this PermitRootLogin line | /etc/ssh/sshd_config line 2: unsupported option "maybe".` {
				t.Errorf("%s: %s", tc.name, got)
			}
		case "a drop-in with a bad value":
			if got := text(0); got != `sshd does not accept this PermitRootLogin line | /etc/ssh/sshd_config.d/50-local.conf line 2: unsupported option "maybe".` {
				t.Errorf("%s: %s", tc.name, got)
			}
		case "a Match block":
			if got := text(0); got != `sshd does not accept this Port line | /etc/ssh/sshd_config line 3: Directive 'Port' is not allowed within a Match block` {
				t.Errorf("%s: %s", tc.name, got)
			}
		case "a Match condition":
			// A line sshd printed before the one with the line number
			// belongs to it.
			if got := text(0); got != `sshd does not accept this Match line | Unsupported Match attribute Usr; /etc/ssh/sshd_config line 2: Bad Match condition` {
				t.Errorf("%s: %s", tc.name, got)
			}
		case "an unknown keyword in a Match block":
			if got := text(0); got != `unknown option "PermitRootLogn" | /etc/ssh/sshd_config: line 2: Bad configuration option: PermitRootLogn; /etc/ssh/sshd_config line 2: Directive 'PermitRootLogn' is not allowed within a Match block` {
				t.Errorf("%s: %s", tc.name, got)
			}
		case "a setting without its partner":
			if got := text(0); got != "sshd refuses the configuration, without naming a line | AuthorizedKeysCommand set without AuthorizedKeysCommandUser" {
				t.Errorf("%s: %s", tc.name, got)
			}
		}
	}
}

// A HostKey line naming a file that does not exist, when sshd has no
// other key, stops the server: a finding on that line. As root sshd
// reads the machine's keys, so this is what -h would hide.
func TestSshdHostKeyMissing(t *testing.T) {
	for _, data := range []string{
		"Port 22\nHostKey /nonexistent/key\n",
		"Port 22\nhostkey=/nonexistent/key\n",
		"Port 22\n\tHostKey \"/nonexistent/key\"\n",
	} {
		c, _ := sshdMachine(t, nil, "testdata/sshd/nokey.keymissing.user", 1)
		rep, err := c.Check(context.Background(), sshdConfig, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != "2 sshd-invalid blocker" || len(rep.Notes) != 0 {
			t.Errorf("%q: %q %q", data, got, rep.Notes)
			continue
		}
		if f := rep.Findings[0]; f.Text != "host key /nonexistent/key does not exist, and sshd has no other" ||
			f.Raw != "Unable to load host key: /nonexistent/key; sshd: no hostkeys available -- exiting." {
			t.Errorf("%q: %+v", data, f)
		}
	}
}

// The real sshd is not run with a key: this machine's are what count.
// A test's sshd gets a throwaway one with -h, so it works as a user.
func TestSshdHostKeyArg(t *testing.T) {
	c, args := sshdMachine(t, nil, "testdata/sshd/good.user", 0)
	c.sshdHostKey = "/tmp/no/such/key"
	if _, err := c.Check(context.Background(), sshdConfig, []byte("Port 22\n")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(args)
	if a := strings.Split(strings.TrimSpace(string(b)), "\n"); len(a) != 5 || a[3] != "-h" || a[4] != "/tmp/no/such/key" {
		t.Errorf("sshd arguments %q", a)
	}
}

// Two lines sshd refuses with the same keyword and different messages
// are two findings to Added; the same message twice is one, counted.
func TestSshdKeys(t *testing.T) {
	dir := t.TempDir()
	run := func(data string, msgs ...string) []Finding {
		var errOut string
		for i, m := range msgs {
			if m != "" {
				errOut += fmt.Sprintf("@FILE@ line %d: %s\r\n", i+1, m)
			}
		}
		os.WriteFile(filepath.Join(dir, "g.out"), nil, 0o644)
		os.WriteFile(filepath.Join(dir, "g.err"), []byte(errOut), 0o644)
		c, _ := sshdMachine(t, nil, filepath.Join(dir, "g"), 255)
		rep, err := c.Check(context.Background(), sshdConfig, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	before := run("Port abc\n", "Badly formatted port number.")
	if got := brief(Added(before, run("Port\n", `no argument after keyword "Port"`))); got != "1 sshd-invalid blocker" {
		t.Errorf("another message for the same keyword: %q", got)
	}
	if got := brief(Added(before, run("# moved\nPort abc\n", "", "Badly formatted port number."))); got != "" {
		t.Errorf("the same line moved: %q", got)
	}
	if got := brief(Added(before, run("Port abc\nPort abc\n", "Badly formatted port number.", "Badly formatted port number."))); got != "2 sshd-invalid blocker" {
		t.Errorf("the same line twice: %q", got)
	}
}

// Environment failures as root, which a user cannot provoke (from
// sshd.c: the privilege separation checks come after the file is parsed,
// before -t says it is fine), and an sshd that fails without a word: notes.
func TestSshdEnvironment(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, errOut string
		code         int
		note         string
	}{
		{"privsep dir", "Missing privilege separation directory: /run/sshd\n", 255,
			"sshd did not finish checking the file (Missing privilege separation directory: /run/sshd)"},
		{"privsep user", "Privilege separation user sshd does not exist\n", 255,
			"sshd did not finish checking the file (Privilege separation user sshd does not exist)"},
		{"privsep mode", "/run/sshd must be owned by root and not group or world-writable.\n", 255,
			"sshd did not finish checking the file (/run/sshd must be owned by root and not group or world-writable.)"},
		{"silent", "", 1, "sshd failed (exit 1) without naming a problem; the file was not checked"},
		{"an exit 1 sc has not seen", "something new\n", 1, "sshd could not check the file (something new)"},
	} {
		os.WriteFile(filepath.Join(dir, "g.out"), nil, 0o644)
		os.WriteFile(filepath.Join(dir, "g.err"), []byte(tc.errOut), 0o644)
		c, _ := sshdMachine(t, nil, filepath.Join(dir, "g"), tc.code)
		rep, err := c.Check(context.Background(), sshdConfig, []byte("Port 22\n"))
		if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s: %q %q %v", tc.name, brief(rep.Findings), rep.Notes, err)
		}
	}
}

// No sshd, an sshd that was killed, and one that hangs: a note, never a
// clean run.
func TestSshdBroken(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	data := []byte("PermitRootLogn no\n")
	rep, err := c.Check(context.Background(), sshdConfig, data)
	if err != nil || rep.Checker != "sshd" || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != "no validator found (sshd); only sc's own rules ran" {
		t.Errorf("missing: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "sshd"), []byte("#!/bin/sh\necho \"$3: line 1: Bad configuration option: PermitRootLogn\" >&2\nkill -9 $$\n"), 0o755)
	rep, err = c.Check(context.Background(), sshdConfig, data)
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != "sshd was killed; only sc's own rules ran" {
		t.Errorf("killed: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "sshd"), []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err = c.Check(context.Background(), sshdConfig, data)
	if err != nil || len(rep.Findings) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "sshd did not finish") {
		t.Errorf("timeout: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

// The real sshd, where installed, with a throwaway key made here: its
// messages for fabricated content parse as the golden ones do. Without a
// key, as a user, the file is not checked to the end.
func TestSshdReal(t *testing.T) {
	for _, p := range []string{"/usr/sbin/sshd", "/usr/bin/ssh-keygen"} {
		if _, err := os.Stat(p); err != nil {
			t.Skip("no " + p)
		}
	}
	key := filepath.Join(t.TempDir(), "key")
	if out, err := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome"), sshdHostKey: key}
	// As root, sshd checks its privilege separation directory after the
	// file; where ssh.service never ran (a CI runner) there is none, and a
	// clean file is then a note, not a clean run.
	cleanNotes := ""
	if _, err := os.Stat("/run/sshd"); err != nil && os.Geteuid() == 0 {
		cleanNotes = "sshd did not finish checking the file (Missing privilege separation directory: /run/sshd)"
	}
	for _, tc := range []struct {
		path, data, want string
	}{
		{sshdConfig, "Port 22\nPermitRootLogin no\nSubsystem sftp /usr/lib/openssh/sftp-server\n", ""},
		{sshdConfig, badLines, "2 sshd-invalid blocker\n3 sshd-invalid blocker"},
		{sshdDropIn, "PasswordAuthentication no\nPermitRootLogn no\n", "2 sshd-invalid blocker"},
		{sshdConfig, "Port 22\nRSAAuthentication yes\n", ""},
	} {
		rep, err := c.Check(context.Background(), tc.path, []byte(tc.data))
		if err != nil {
			t.Fatal(err)
		}
		wantNotes := ""
		if tc.want == "" {
			wantNotes = cleanNotes
		}
		if got := brief(rep.Findings); got != tc.want || strings.Join(rep.Notes, "|") != wantNotes {
			t.Errorf("%q:\n%s\nwant:\n%s\nnotes %q", tc.data, got, tc.want, rep.Notes)
		}
		if tc.want != "" && !strings.HasPrefix(rep.Findings[0].Raw, tc.path) {
			t.Errorf("raw %q", rep.Findings[0].Raw)
		}
	}
	if os.Geteuid() == 0 {
		return
	}
	c.sshdHostKey = ""
	rep, err := c.Check(context.Background(), sshdConfig, []byte("Port 22\n"))
	if err != nil || len(rep.Findings) != 0 || strings.Join(rep.Notes, "|") != noteNoKeys {
		t.Errorf("no key, as a user: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	// Its parse comes first: a bad line is found without a key.
	rep, err = c.Check(context.Background(), sshdConfig, []byte(badLines))
	if err != nil || brief(rep.Findings) != "2 sshd-invalid blocker\n3 sshd-invalid blocker" || len(rep.Notes) != 0 {
		t.Errorf("no key, bad lines: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

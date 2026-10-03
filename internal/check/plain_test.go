//go:build linux

package check

import (
	"context"
	"strings"
	"testing"
)

// plainCase is one file for a checker that runs no validator: the
// findings as brief() prints them, and the notes.
type plainCase struct {
	name, data string
	want, note string
}

// runPlain checks each case's data as path on a machine where only have
// exists, and compares findings and notes.
func runPlain(t *testing.T, path string, have []string, cases []plainCase) {
	t.Helper()
	c, _ := fakeMachine(t, have, "", "", 0)
	for _, tc := range cases {
		rep, err := c.Check(context.Background(), path, []byte(tc.data))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := brief(rep.Findings); got != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		for _, f := range rep.Findings {
			if _, ok := Lookup(f.Rule); !ok || f.Path != path || strings.Contains(f.Text, "\n") {
				t.Errorf("%s: finding %+v", tc.name, f)
			}
		}
	}
}

const ubuntuNsswitch = "# /etc/nsswitch.conf\n#\n# Example configuration of GNU Name Service Switch functionality.\n\n" +
	"passwd:         files systemd\ngroup:          files systemd\nshadow:         files systemd\ngshadow:        files systemd\n\n" +
	"hosts:          files mdns4_minimal [NOTFOUND=return] dns\nnetworks:       files\n\n" +
	"protocols:      db files\nservices:       db files\nethers:         db files\nrpc:            db files\n\nnetgroup:       nis\n"

const nssBase = "passwd: files\ngroup: files\nshadow: files\n"

func TestNsswitch(t *testing.T) {
	runPlain(t, "/etc/nsswitch.conf", nil, []plainCase{
		{name: "ubuntu's file", data: ubuntuNsswitch},
		{name: "empty file"},
		{name: "only comments", data: "# nothing\n#passwd: nis\n"},
		{name: "no passwd line: glibc's default is files", data: "hosts: files dns\n"},
		{name: "compat", data: "passwd: compat\ngroup: compat\nshadow: compat\n"},
		{name: "files last", data: "passwd: sss [NOTFOUND=return] files\n"},
		{name: "sssd", data: "passwd: files systemd sss\ngroup: files systemd sss\nshadow: files systemd sss\n"},
		{name: "tabs, no space after the colon, trailing blanks", data: "passwd:\tfiles\t\ngroup:files   \n"},
		{name: "leading blanks and a colon with blanks before it", data: "   passwd : files\n"},
		{name: "no colon at all", data: "passwd files\n"},
		{name: "CRLF", data: "passwd: files\r\ngroup: files\r\n"},
		{name: "a final line without newline", data: "passwd: files\ngroup: files"},
		{name: "a database name in another case is not glibc's", data: "Passwd: nis\nPASSWD: nis\n"},
		{name: "files after a # is still a service to glibc", data: "passwd: nis # files\n"},
		{name: "an action glued to the service", data: "passwd: files[SUCCESS=return] nis\n"},
		{name: "blanks inside the action", data: "passwd: nis [ NOTFOUND = return ] files\n"},
		{name: "the last passwd line wins, files", data: "passwd: nis\npasswd: files\n"},
		{name: "a final passwd line without newline is never read: the default applies", data: "passwd: files\npasswd: nis"},

		{name: "nis only", data: "passwd: nis\n", want: "1 nsswitch-no-files blocker"},
		// Actions, each checked against glibc 2.39 on the dev VM (getent
		// passwd root with the file bind-mounted in a private mount
		// namespace): one bad action anywhere and every lookup fails.
		{name: "a bad action on the hosts line", data: nssBase + "hosts: files [NOTFOUND=retrun] dns\n", want: "4 nsswitch-invalid blocker"},
		{name: "an unclosed bracket", data: nssBase + "hosts: files [NOTFOUND=return dns\n", want: "4 nsswitch-invalid blocker"},
		{name: "a bad action after #", data: nssBase + "hosts: files dns # [NOTFOUND=retrun]\n", want: "4 nsswitch-invalid blocker"},
		{name: "a status without =", data: nssBase + "hosts: files [NOTFOUND] dns\n", want: "4 nsswitch-invalid blocker"},
		{name: "empty brackets", data: nssBase + "hosts: files [] dns\n", want: "4 nsswitch-invalid blocker"},
		{name: "a bad action and no local source", data: "passwd: nis [NOTFOUND=retrun]\n", want: "1 nsswitch-invalid blocker\n1 nsswitch-no-files blocker"},
		{name: "actions in any case", data: nssBase + "hosts: files [notfound=RETURN] dns\n"},
		{name: "! and blanks in an action", data: nssBase + "hosts: files [ !UNAVAIL = return ] dns\n"},
		{name: "two pairs in one action", data: nssBase + "hosts: files [NOTFOUND=return UNAVAIL=continue] dns\n"},
		{name: "merge", data: nssBase + "hosts: files [SUCCESS=merge] dns\n"},
		{name: "a bad action on a # line", data: nssBase + "# hosts: files [NOTFOUND=retrun]\n"},
		{name: "a bad action for a database glibc does not know", data: nssBase + "sudoers: files [NOTFOUND=retrun]\n"},
		{name: "a bad action on a last line with no newline", data: nssBase + "hosts: files [x]"},
		{name: "a bracket before any service", data: nssBase + "hosts: [NOTFOUND=retrun] files\n"},
		// nss-systemd gives root and nobody, not the users in /etc/passwd.
		{name: "systemd alone", data: "passwd: systemd\n", want: "1 nsswitch-no-files blocker"},
		{name: "a misspelt files", data: "passwd:         fiels sss\n", want: "1 nsswitch-no-files blocker"},
		{name: "FILES is not a module on disk", data: "passwd: FILES\n", want: "1 nsswitch-no-files blocker"},
		{name: "nothing after the colon", data: "group: files\npasswd:\n", want: "2 nsswitch-no-files blocker"},
		{name: "nothing after the colon, CRLF", data: "passwd:\r\n", want: "1 nsswitch-no-files blocker"},
		{name: "a bare name: glibc reads an empty service list", data: "passwd\n", want: "1 nsswitch-no-files blocker"},
		{name: "the last passwd line wins, nis", data: "passwd: files\n\npasswd: nis\n", want: "3 nsswitch-no-files blocker"},
		{name: "an action with no service before it", data: "passwd: [NOTFOUND=return] files\n", want: "1 nsswitch-no-files blocker"},
		{name: "group and shadow at error", data: "passwd: files\ngroup: sss\nshadow: ldap\n",
			want: "2 nsswitch-no-files error\n3 nsswitch-no-files error"},
		{name: "all three", data: "passwd: ldap\ngroup: ldap\nshadow: ldap\n",
			want: "1 nsswitch-no-files blocker\n2 nsswitch-no-files error\n3 nsswitch-no-files error"},
	})
	c, _ := fakeMachine(t, nil, "", "", 0)
	rep, err := c.Check(context.Background(), "/etc/nsswitch.conf", []byte("passwd: nis\ngroup: nis\nshadow: nis\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"the passwd line names neither files nor compat, so the users in /etc/passwd cannot be looked up",
		"the group line names neither files nor compat, so local groups (sudo included) and their members are lost",
		"the shadow line names neither files nor compat, so local passwords cannot be checked",
	}
	for i, f := range rep.Findings {
		if i >= len(want) || f.Text != want[i] {
			t.Errorf("text %d: %q", i, f.Text)
		}
	}
	if rep.Checker != "nsswitch" || len(rep.Notes) != 0 {
		t.Errorf("checker %q notes %q", rep.Checker, rep.Notes)
	}
}

func TestPreload(t *testing.T) {
	have := []string{"/usr/lib/x86_64-linux-gnu/libfakeroot/libfakeroot-0.so", "/usr/local/lib/libeat.so", "/usr/lib/libthere.so"}
	noPath := "the loader looks for libfoo.so itself (no full path, or a $TOKEN); sc did not check it"
	runPlain(t, "/etc/ld.so.preload", have, []plainCase{
		{name: "empty file"},
		{name: "only comments", data: "# nothing yet\n\n   # really\n"},
		{name: "one library", data: "/usr/lib/x86_64-linux-gnu/libfakeroot/libfakeroot-0.so\n"},
		{name: "no final newline", data: "/usr/local/lib/libeat.so"},
		{name: "colons, spaces and tabs", data: "/usr/local/lib/libeat.so:/usr/lib/libthere.so \t/usr/local/lib/libeat.so\n"},
		{name: "a comment after the name", data: "/usr/lib/libthere.so # for tests\n"},
		{name: "a bare name is looked up by the loader", data: "libfoo.so\n", note: noPath},
		{name: "a token", data: "/usr/$LIB/libfoo.so ${ORIGIN}/x.so\n",
			note: "the loader looks for /usr/$LIB/libfoo.so, ${ORIGIN}/x.so itself (no full path, or a $TOKEN); sc did not check it"},
		{name: "a relative path", data: "lib/libfoo.so\n",
			note: "the loader looks for lib/libfoo.so itself (no full path, or a $TOKEN); sc did not check it"},

		{name: "a missing library", data: "/usr/lib/libgone.so\n", want: "1 preload-missing-lib blocker"},
		{name: "missing, no final newline", data: "/usr/lib/libgone.so", want: "1 preload-missing-lib blocker"},
		{name: "one of three, on line 3", data: "# comment\n/usr/lib/libthere.so\n/usr/local/lib/libeat.so:/usr/lib/libgone.so\n",
			want: "3 preload-missing-lib blocker"},
		{name: "two missing, one twice", data: "/usr/lib/libgone.so\n/usr/lib/libgone.so /usr/lib/libgone2.so\n",
			want: "1 preload-missing-lib blocker\n2 preload-missing-lib blocker"},
		{name: "CRLF makes the carriage return part of the name", data: "/usr/lib/libthere.so\r\n", want: "1 preload-missing-lib blocker"},
		{name: "CRLF with a blank before the end: the loader looks for a library named CR", data: "/usr/lib/libthere.so \r\n", want: "1 preload-missing-lib blocker"},
		{name: "a missing library and a bare one", data: "libfoo.so\n/usr/lib/libgone.so\n", want: "2 preload-missing-lib blocker", note: noPath},
		{name: "a comment hides the rest of the line", data: "/usr/lib/libgone.so # /usr/lib/libgone2.so\n", want: "1 preload-missing-lib blocker"},
	})
	c, _ := fakeMachine(t, have, "", "", 0)
	for data, want := range map[string]string{
		"/usr/lib/libgone.so\n":    "preloaded library /usr/lib/libgone.so does not exist on this machine",
		"/usr/lib/libthere.so\r\n": `preloaded library "/usr/lib/libthere.so\r" does not exist on this machine (its name ends in a carriage return: a DOS line ending)`,
	} {
		rep, err := c.Check(context.Background(), "/etc/ld.so.preload", []byte(data))
		if err != nil || len(rep.Findings) != 1 || rep.Findings[0].Text != want {
			t.Errorf("%q: %+v %v", data, rep.Findings, err)
		}
	}
}

// A flag file is a finding by existing; the rule comes from its name.
func TestFlag(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	for path, want := range map[string]string{
		"/etc/nologin":                "/etc/nologin exists, so only root can log in until it is deleted",
		"/etc/ssh/sshd_not_to_be_run": "/etc/ssh/sshd_not_to_be_run exists, so the SSH server does not start until it is deleted",
	} {
		for _, data := range []string{"", "System going down for maintenance\n"} {
			rep, err := c.Check(context.Background(), path, []byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Findings) != 1 || rep.Findings[0].Line != 0 || rep.Findings[0].Severity != Warning ||
				rep.Findings[0].Text != want || rep.Checker != "flag" || len(rep.Notes) != 0 {
				t.Errorf("%s %q: %+v %q", path, data, rep.Findings, rep.Notes)
			}
			if rep.Findings[0].Rule != "flag-"+strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(path, "/etc/ssh/"), "/etc/"), "_", "-") {
				t.Errorf("%s: rule %s", path, rep.Findings[0].Rule)
			}
		}
	}
	g, _ := ParseGraph("check flag /etc/other")
	c.Graph = g
	if _, err := c.Check(context.Background(), "/etc/other", nil); err == nil || !strings.Contains(err.Error(), "no rule for /etc/other") {
		t.Errorf("a flag the checker does not know: %v", err)
	}
}

func TestHosts(t *testing.T) {
	const none = "0 hosts-no-localhost warning"
	runPlain(t, "/etc/hosts", nil, []plainCase{
		{name: "ubuntu's file", data: "127.0.0.1 localhost\n127.0.1.1 myhost\n\n# The following lines are desirable for IPv6 capable hosts\n" +
			"::1     ip6-localhost ip6-loopback\nfe00::0 ip6-localnet\nff02::1 ip6-allnodes\nff02::2 ip6-allrouters\n"},
		{name: "ipv6 only", data: "::1 localhost\n"},
		{name: "ipv6 alias", data: "::1 ip6-localhost localhost ip6-loopback\n"},
		{name: "an alias", data: "127.0.0.1 myhost localhost\n"},
		{name: "elsewhere in 127/8", data: "127.0.1.1 localhost\n"},
		{name: "long ipv6 form", data: "0:0:0:0:0:0:0:1 localhost\n"},
		{name: "tabs and blanks", data: "\t127.0.0.1\t\tlocalhost   \n"},
		{name: "upper case: names are compared case-insensitively", data: "127.0.0.1 LOCALHOST\n"},
		{name: "a comment after the names", data: "127.0.0.1 localhost # the usual\n"},
		{name: "CRLF", data: "127.0.0.1 localhost\r\n"},
		{name: "no final newline", data: "# hosts\n127.0.0.1 localhost"},

		{name: "empty file", want: none},
		{name: "only comments", data: "# 127.0.0.1 localhost\n", want: none},
		{name: "commented out after the address", data: "127.0.0.1 # localhost\n", want: none},
		{name: "not a loopback address", data: "192.168.1.5 localhost\n::2 localhost\n", want: none},
		{name: "another name only", data: "127.0.0.1 myhost\n::1 ip6-localhost ip6-loopback\n", want: none},
		{name: "localhost.localdomain is another name", data: "127.0.0.1 localhost.localdomain\n", want: none},
		{name: "not an address, so glibc drops the line", data: "127.1 localhost\nlocalhost 127.0.0.1\n", want: none},
		{name: "an address alone", data: "127.0.0.1\n", want: none},
	})
	c, _ := fakeMachine(t, nil, "", "", 0)
	rep, err := c.Check(context.Background(), "/etc/hosts", nil)
	if err != nil || len(rep.Findings) != 1 || rep.Findings[0].Text != "no line maps localhost to 127.0.0.1 or ::1" {
		t.Errorf("%+v %v", rep.Findings, err)
	}
}

// The parser's view of a file, for the cases a finding cannot show.
func TestParseNsswitch(t *testing.T) {
	got := parseNsswitch([]byte("passwd: files [NOTFOUND=return] nis # x\n group :compat\nhosts: files [!UNAVAIL=return]dns\nshadow\nbad\n"))
	want := map[string]string{"passwd": "1 files nis # x", "group": "2 compat", "hosts": "3 files dns", "shadow": "4", "bad": "5"}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
	for db, w := range want {
		l := got[db]
		if s := strings.TrimSpace(strings.Join(append([]string{string(rune('0' + l.line))}, l.services...), " ")); s != w {
			t.Errorf("%s: %q, want %q", db, s, w)
		}
	}
}

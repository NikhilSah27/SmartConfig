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

// fakeMachine is a Checks on a machine where only the paths in have exist,
// and whose only validator is a script named tool that records its
// arguments, prints the golden file and exits with code. tool "" gives a
// machine with no validators.
func fakeMachine(t *testing.T, have []string, tool, golden string, code int) (*Checks, string) {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	if tool != "" {
		script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > %s\ncat %s\nexit %d\n", args, golden, code)
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	set := map[string]bool{}
	for _, p := range have {
		set[p] = true
	}
	return &Checks{Home: filepath.Join(t.TempDir(), "schome"), Run: Runner{Dirs: []string{dir}},
		exists: func(p string) bool { return set[p] }}, args
}

// brief is "line rule severity" for each finding.
func brief(fs []Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%d %s %s", f.Line, f.Rule, f.Severity))
	}
	return strings.Join(out, "\n")
}

const rootUUID = "/dev/disk/by-uuid/aaaaaaaa-0000-4000-8000-000000000001"

// sc's own fstab rules, on a machine without findmnt.
func TestFstabRules(t *testing.T) {
	c, _ := fakeMachine(t, []string{rootUUID, "/dev/sda3", "/dev/disk/by-uuid/ABCD-1234", "/dev/disk/by-label/data"}, "", "", 0)
	fstab := strings.Join([]string{
		"# a comment", // 1
		"UUID=aaaaaaaa-0000-4000-8000-000000000001 / ext4 errors=remount-ro 0 1", // 2 fine
		"UUID=bbbbbbbb-0000-4000-8000-000000000002 /data ext4 defaults 0 2",      // 3 blocker
		"UUID=bbbbbbbb-0000-4000-8000-000000000002 /opt/a ext4 defaults,nofail",  // 4 warning
		"/dev/sdz9 /opt/b ext4 noauto 0 0",                                       // 5 warning
		"UUID=bbbbbbbb-0000-4000-8000-000000000003 none swap sw 0 0",             // 6 error
		"",                                // 7
		"/dev/sda3 /srv ext4 defalts 0 2", // 8 typo, blocker
		`UUID="abcd-1234" /boot/efi vfat umask=0077,x-systemd.automount 0 1`, // 9 fine (case, quotes)
		`LABEL=My\040Disk /mnt/c ext4 defaults 0 2`,                          // 10 not checkable
		"LABEL=data /mnt/d ext4 noatime,nofial 0 2",                          // 11 typo, blocker
		"LABEL=gone /mnt/e ext4 defaults 0 2",                                // 12 blocker
		"server:/export /mnt/nfs nfs defaults,_netdev,usres=hunter2 0 0",     // 13 typo, error
		"tmpfs /tmp tmpfs size=1G,mode=1777 0 0",                             // 14 fine
		"/srv/a /srv/b none bind 0 0",                                        // 15 fine
		"onefield",                                                           // 16 fields
		"/dev/sda3 /x",                                                       // 17 fields
		"/dev/sda3 /y ext4 defaults x 2",                                     // 18 fields
		"/dev/sda3 /z ext4",                                                  // 19 fine: three fields
	}, "\n") + "\n"
	rep, err := c.Check(context.Background(), "/etc/fstab", []byte(fstab))
	if err != nil {
		t.Fatal(err)
	}
	want := `3 fstab-source-missing blocker
4 fstab-source-missing warning
5 fstab-source-missing warning
6 fstab-source-missing error
8 fstab-option-typo blocker
11 fstab-option-typo blocker
12 fstab-source-missing blocker
13 fstab-option-typo error
16 fstab-fields error
17 fstab-fields error
18 fstab-fields error`
	if got := brief(rep.Findings); got != want {
		t.Errorf("findings:\n%s\nwant:\n%s", got, want)
	}
	if rep.Checker != "fstab" || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "no validator found (findmnt)") {
		t.Errorf("checker %q notes %q", rep.Checker, rep.Notes)
	}
	for _, f := range rep.Findings {
		if f.Path != "/etc/fstab" || strings.Contains(f.Text, "hunter2") || strings.Contains(f.Text, c.Home) {
			t.Errorf("finding %+v", f)
		}
		if _, ok := Lookup(f.Rule); !ok {
			t.Errorf("rule %s is not in the table", f.Rule)
		}
	}
	if got := rep.Findings[4].Text; got != `option "defalts" of /srv looks like a misspelling of "defaults"` {
		t.Errorf("typo text: %s", got)
	}
	if got := rep.Findings[0].Text; got != "UUID=bbbbbbbb-0000-4000-8000-000000000002 (for /data) is not a device on this machine" {
		t.Errorf("missing text: %s", got)
	}

	// The root line's own rule.
	rep, err = c.Check(context.Background(), "/etc/fstab", []byte("UUID=cccccccc-0000-4000-8000-000000000009 / ext4 defaults 0 1\n"))
	if err != nil || brief(rep.Findings) != "1 fstab-root-source error" {
		t.Errorf("root line: %q %v", brief(rep.Findings), err)
	}
}

// findmnt's output, as captured on the dev VM from fabricated files: its
// missing-source and missing-mount-point lines add nothing to sc's rules,
// its on-disk type warning and unknown errors do.
func TestFstabFindmnt(t *testing.T) {
	mixed := "UUID=aaaaaaaa-0000-4000-8000-000000000001 / ext4 defaults 0 1\n" +
		"UUID=11111111-2222-3333-4444-555555555555 /data ext4 defaults 0 2\n" +
		"/dev/sdz9 /mnt/x ext4 defaults,nofail 0 2\nonefield\n"
	unknown := filepath.Join(t.TempDir(), "unknown.txt")
	os.WriteFile(unknown, []byte("\n0 parse errors, 1 error, 0 warnings\n/data\n   [E] something findmnt learns to say later\n"), 0o644)
	for _, tc := range []struct {
		golden, fstab string
		code          int
		want          string
	}{
		{"testdata/findmnt/mixed.user.txt", mixed, 1, "2 fstab-source-missing blocker\n3 fstab-source-missing warning\n4 fstab-fields error"},
		{"testdata/findmnt/mixed.root.txt", mixed, 1, "2 fstab-source-missing blocker\n3 fstab-source-missing warning\n4 fstab-fields error"},
		{"testdata/findmnt/good.root.txt", mixed[:strings.Index(mixed, "\n")+1], 0, ""},
		{"testdata/findmnt/good.user.txt", mixed[:strings.Index(mixed, "\n")+1], 0, ""},
		{"testdata/findmnt/badtype.root.txt", "UUID=aaaaaaaa-0000-4000-8000-000000000001 / xfs defaults 0 1\n", 0, "1 fstab-fstype-mismatch error"},
		{unknown, "/dev/sda3 /data ext4 defaults 0 2\n", 1, "1 fstab-verify warning"},
	} {
		golden, _ := filepath.Abs(tc.golden)
		c, args := fakeMachine(t, []string{rootUUID, "/dev/sda3"}, "findmnt", golden, tc.code)
		rep, err := c.Check(context.Background(), "/etc/fstab", []byte(tc.fstab))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || len(rep.Notes) != 0 {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q", tc.golden, got, tc.want, rep.Notes)
		}
		// findmnt was given exactly the check-only form, on a scratch copy
		// that is gone afterwards.
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 3 || a[0] != "--verify" || a[1] != "--tab-file" || filepath.Base(a[2]) != "fstab" ||
			!strings.HasPrefix(a[2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: findmnt arguments %q", tc.golden, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.golden, left)
		}
		if strings.Contains(tc.golden, "badtype") {
			if f := rep.Findings[0]; f.Text != "/ is ext4 on disk, not xfs" || f.Raw != "[W] xfs does not match with on-disk ext4" {
				t.Errorf("mismatch finding %+v", f)
			}
		}
	}
}

// A findmnt that hangs costs a note, not the check.
func TestFstabFindmntTimeout(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "findmnt"), []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	c.Run.Timeout = 200 * time.Millisecond
	rep, err := c.Check(context.Background(), "/etc/fstab", []byte("/dev/sdz9 /data ext4 defaults 0 2\n"))
	if err != nil || brief(rep.Findings) != "1 fstab-source-missing blocker" ||
		len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "findmnt did not finish") {
		t.Fatalf("%q %q %v", brief(rep.Findings), rep.Notes, err)
	}
}

// The real findmnt, where installed: its output for a missing device and a
// bad line parses into nothing but sc's own findings.
func TestFstabRealFindmnt(t *testing.T) {
	if _, err := os.Stat("/usr/bin/findmnt"); err != nil {
		t.Skip("no findmnt")
	}
	c := &Checks{Home: filepath.Join(t.TempDir(), "schome")}
	rep, err := c.Check(context.Background(), "/etc/fstab", []byte("/dev/sc-no-such-disk /data ext4 defaults 0 2\nonefield\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := brief(rep.Findings); got != "1 fstab-source-missing blocker\n2 fstab-fields error" || len(rep.Notes) != 0 {
		t.Errorf("findings:\n%s\nnotes %q", got, rep.Notes)
	}
}

// A file no checker reads gives an empty report; a graph that names a
// checker with no code is an error; every checker of the built-in graph
// has code.
func TestCheckDispatch(t *testing.T) {
	c := &Checks{Home: t.TempDir()}
	if rep, err := c.Check(context.Background(), "/etc/hostname", []byte("x\n")); err != nil || rep.Checker != "" || rep.Findings != nil {
		t.Errorf("no checker: %+v %v", rep, err)
	}
	g, _ := ParseGraph("check nosuch /etc/x")
	c.Graph = g
	if _, err := c.Check(context.Background(), "/etc/x", nil); err == nil || !strings.Contains(err.Error(), "checker nosuch, which does not exist") {
		t.Errorf("missing checker: %v", err)
	}
	for _, name := range DefaultGraph().Checkers() {
		if checkers[name] == nil {
			t.Errorf("the built-in graph names checker %s, which has no code", name)
		}
	}
}

func TestOptionTypo(t *testing.T) {
	for o, want := range map[string]string{
		"defalts": "defaults", "defualts": "defaults", "default": "defaults", "nofial": "nofail", "noatme": "noatime",
		"nosiud": "nosuid", "usres": "users", "erors": "errors", "netdev": "_netdev",
		"defaults": "", "noatime": "", "users": "", "user": "", "x-systemd.automount": "", "X-mount.mkdir": "",
		"comment": "", "ro": "", "rw": "", "sw": "", "noload": "", "noacl": "", "nodiscard": "", "commit": "",
		"subvol": "", "compress": "", "uid": "", "somethingelse": "", "bind": "",
	} {
		if got := optionTypo(o); got != want {
			t.Errorf("optionTypo(%q) = %q, want %q", o, got, want)
		}
	}
	for _, tc := range []struct {
		a, b string
		d    int
	}{{"", "", 0}, {"a", "", 1}, {"abc", "abc", 0}, {"abc", "acb", 1}, {"defalts", "defaults", 1}, {"kitten", "sitting", 3}} {
		if got := editDistance(tc.a, tc.b); got != tc.d {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.d)
		}
	}
}

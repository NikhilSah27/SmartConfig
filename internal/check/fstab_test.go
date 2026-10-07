//go:build linux

package check

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMachine is a Checks on a machine where only the paths in have exist,
// and whose only validator is a script named tool that records its
// arguments, prints golden.out to stdout and golden.err to stderr and exits
// with code. tool "" gives a machine with no validators.
func fakeMachine(t *testing.T, have []string, tool, golden string, code int) (*Checks, string) {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	if tool != "" {
		script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > %s\ncat %s.out\ncat %s.err >&2\nexit %d\n", args, golden, golden, code)
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	set := map[string]bool{}
	for _, p := range have {
		set[p] = true
	}
	return &Checks{Home: filepath.Join(t.TempDir(), "schome"), Run: Runner{Dirs: []string{dir}},
		nsswitchPath: filepath.Join(dir, "no-nsswitch.conf"), // no nss-systemd: the worse case
		groupPath:    filepath.Join(dir, "no-group"),         // no admins to look for
		passwdPath:   filepath.Join(dir, "no-passwd"),
		addrs:        func() ([]netip.Addr, error) { return fakeAddrs, nil },
		sshdRoot:     filepath.Join(dir, "no-root"), // no sshd_config: a drop-in is checked alone
		exists:       func(p string) bool { return set[p] }}, args
}

// fakeAddrs are a fake machine's interface addresses.
var fakeAddrs = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1"),
	netip.MustParseAddr("10.0.2.15"), netip.MustParseAddr("fe80::5054:ff:fe12:3456")}

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
	upper := strings.ToUpper("aaaaaaaa-0000-4000-8000-000000000001")
	fstab := strings.Join([]string{
		"# a comment", // 1
		"UUID=aaaaaaaa-0000-4000-8000-000000000001 / ext4 errors=remount-ro 0 1", // 2 fine
		"UUID=bbbbbbbb-0000-4000-8000-000000000002 /data ext4 defaults 0 2",      // 3 blocker
		"UUID=bbbbbbbb-0000-4000-8000-000000000002 /opt/a ext4 defaults,nofail",  // 4 warning
		"/dev/sdz9 /opt/b ext4 noauto 0 0",                                       // 5 warning
		"UUID=bbbbbbbb-0000-4000-8000-000000000003 none swap sw 0 0",             // 6 error
		"",                                // 7
		"/dev/sda3 /srv ext4 defalts 0 2", // 8 typo, blocker
		`UUID="ABCD-1234" /boot/efi vfat umask=0077,x-systemd.automount 0 1`, // 9 fine (quotes)
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
		"UUID=" + upper + " /up ext4 defaults 0 2",                           // 20 blocker: tags are case-sensitive
		"LABEL=Data /lbl ext4 defaults 0 2",                                  // 21 blocker: the disk's label is data
		"/dev/sda3 /b btrfs nossd,wsync,resuid=0,nocto,_rnetdev 0 0",         // 22 fine: real options near common ones
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
18 fstab-fields error
20 fstab-source-missing blocker
21 fstab-source-missing blocker`
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

// A newly broken line is found even when the old file already had a line
// that is not an fstab line: the two have different keys.
func TestFstabAddedFields(t *testing.T) {
	c, _ := fakeMachine(t, []string{"/dev/sda3"}, "", "", 0)
	check := func(fstab string) []Finding {
		rep, err := c.Check(context.Background(), "/etc/fstab", []byte(fstab))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	before := check("/dev/sda3 / ext4 defaults 0 1\n\njunk\n")
	if got := brief(Added(before, check("/dev/sda3 / ext4 defaults 0 x\n"))); got != "1 fstab-fields error" {
		t.Errorf("junk removed, root line broken: %q", got)
	}
	if got := brief(Added(before, check("/dev/sda3 / ext4 defaults 0 x\n\njunk\n"))); got != "1 fstab-fields error" {
		t.Errorf("junk kept, root line broken: %q", got)
	}
	if got := brief(Added(before, check("# moved\n/dev/sda3 / ext4 defaults 0 1\njunk\n"))); got != "" {
		t.Errorf("junk moved: %q", got)
	}
}

const (
	noteUnread = "findmnt could not read the disks (not root): filesystem types were not compared"
	rootLine   = "UUID=aaaaaaaa-0000-4000-8000-000000000001 / ext4 defaults 0 1\n"
)

// findmnt's output, as captured on the dev VM from fabricated files (each
// stream in its own file, as the runner reads them).
func TestFstabFindmnt(t *testing.T) {
	mixed := rootLine + "UUID=11111111-2222-3333-4444-555555555555 /data ext4 defaults 0 2\n" +
		"/dev/sdz9 /mnt/x ext4 defaults,nofail 0 2\nonefield\n"
	dir := t.TempDir()
	write := func(name, out, errOut string) string {
		os.WriteFile(filepath.Join(dir, name+".out"), []byte(out), 0o644)
		os.WriteFile(filepath.Join(dir, name+".err"), []byte(errOut), 0o644)
		return filepath.Join(dir, name)
	}
	for _, tc := range []struct {
		name, golden, fstab string
		code                int
		want, note          string
	}{
		// Its missing-source lines for lines sc looked up, and its
		// missing-mount-point lines, add nothing.
		{"mixed, user", "testdata/findmnt/mixed.user", mixed, 1,
			"2 fstab-source-missing blocker\n3 fstab-source-missing warning\n4 fstab-fields error", noteUnread},
		{"mixed, root", "testdata/findmnt/mixed.root", mixed, 1,
			"2 fstab-source-missing blocker\n3 fstab-source-missing warning\n4 fstab-fields error", ""},
		{"good, root", "testdata/findmnt/good.root", rootLine, 0, "", ""},
		{"good, user", "testdata/findmnt/good.user", rootLine, 0, "", noteUnread},
		// The type on disk.
		{"xfs on ext4", "testdata/findmnt/badtype.root", strings.Replace(rootLine, "ext4", "xfs", 1), 0, "1 fstab-fstype-mismatch error", ""},
		{"a list that holds the type", "testdata/findmnt/list.root", strings.Replace(rootLine, "ext4", "ext4,xfs", 1), 0, "", ""},
		{"ext3 on ext4 may mount", "testdata/findmnt/ext3.root", strings.Replace(rootLine, "ext4", "ext3", 1), 0, "1 fstab-fstype-mismatch warning", ""},
		{"ntfs-3g on ntfs", write("ntfs", "/mnt/win\n   [W] ntfs-3g seems unsupported by the current kernel\n   [W] ntfs-3g does not match with on-disk ntfs\n", ""),
			"/dev/sda3 /mnt/win ntfs-3g defaults 0 0\n", 0, "", ""},
		{"xfs on ext4, required disk", write("req", "/data\n   [W] xfs does not match with on-disk ext4\n", ""),
			"/dev/sda3 /data xfs defaults 0 2\n", 0, "1 fstab-fstype-mismatch blocker", ""},
		// Devices sc cannot look up itself: findmnt decides.
		{"unchecked sources", "testdata/findmnt/unchecked.root",
			"LABEL=My\\040Disk /mnt/c ext4 defaults 0 2\nUUID=AAAAAAAA-0000-4000-8000-000000000001 /data ext4 defaults 0 2\n", 1,
			"1 fstab-source-missing blocker\n2 fstab-source-missing blocker", ""},
		// Two lines for one mount point, and a mount point with an escape.
		{"duplicates", "testdata/findmnt/dups.root",
			rootLine + "/dev/sdz8 none swap sw 0 0\n/dev/sdz9 none swap sw,nofail 0 0\n/dev/sdz7 /x\\043y ext4 defaults,nofail 0 2\n", 1,
			"2 fstab-source-missing error\n3 fstab-source-missing warning\n4 fstab-source-missing warning", ""},
		{"a type mismatch on a nofail line with an escape", write("esc", "/x#y\n   [W] xfs does not match with on-disk ext4\n", ""),
			"/dev/sda3 /x\\043y xfs defaults,nofail 0 2\n", 0, "1 fstab-fstype-mismatch warning", ""},
		{"a heading sc cannot match", write("nomatch", "/elsewhere\n   [W] xfs does not match with on-disk ext4\n", ""),
			"/dev/sda3 /data ext4 defaults 0 2\n", 0, "0 fstab-fstype-mismatch error", ""},
		// Errors sc has no rule for.
		{"an unknown error", write("unknown", "/data\n   [E] something findmnt learns to say later\n", "\n0 parse errors, 1 error, 0 warnings\n"),
			"/dev/sda3 /data ext4 defaults 0 2\n", 1, "1 fstab-verify blocker", ""},
		// The line's own severity: with nofail the boot goes on.
		{"an unknown error, nofail", write("unknown2", "/data\n   [E] something findmnt learns to say later\n", "\n0 parse errors, 1 error, 0 warnings\n"),
			"/dev/sda3 /data ext4 defaults,nofail 0 2\n", 1, "1 fstab-verify warning", ""},
		// A findmnt that did not check the file is not a clean run.
		{"usage error", "testdata/findmnt/usage.user", "/dev/sda3 /data ext4 defaults 0 2\n", 1, "",
			"findmnt could not check the file (unrecognized option '--no-such-option'); only sc's own rules ran"},
	} {
		golden, _ := filepath.Abs(tc.golden)
		c, args := fakeMachine(t, []string{rootUUID, "/dev/sda3"}, "findmnt", golden, tc.code)
		rep, err := c.Check(context.Background(), "/etc/fstab", []byte(tc.fstab))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want || strings.Join(rep.Notes, "|") != tc.note {
			t.Errorf("%s:\n%s\nwant:\n%s\nnotes %q, want %q", tc.name, got, tc.want, rep.Notes, tc.note)
		}
		// findmnt was given exactly the check-only form, on a scratch copy
		// that is gone afterwards.
		b, _ := os.ReadFile(args)
		a := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(a) != 3 || a[0] != "--verify" || a[1] != "--tab-file" || filepath.Base(a[2]) != "fstab" ||
			!strings.HasPrefix(a[2], filepath.Join(c.Home, "tmp", "check-")) {
			t.Errorf("%s: findmnt arguments %q", tc.name, a)
		}
		if left, _ := os.ReadDir(filepath.Join(c.Home, "tmp")); len(left) != 0 {
			t.Errorf("%s: scratch copies left: %v", tc.name, left)
		}
		switch tc.name {
		case "xfs on ext4":
			if f := rep.Findings[0]; f.Text != "/ is ext4 on disk, not xfs" || f.Raw != "[W] xfs does not match with on-disk ext4" {
				t.Errorf("mismatch finding %+v", f)
			}
		case "unchecked sources":
			if f := rep.Findings[0]; f.Text != "LABEL=My Disk (for /mnt/c) is not a device on this machine" {
				t.Errorf("unchecked finding %+v", f)
			}
		}
	}
}

// Two unknown findmnt errors for one mount point are two findings to
// Added, although their text is the same.
func TestFstabVerifyKeys(t *testing.T) {
	dir := t.TempDir()
	run := func(msg string) []Finding {
		os.WriteFile(filepath.Join(dir, "g.out"), []byte("/data\n   [E] "+msg+"\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "g.err"), nil, 0o644)
		c, _ := fakeMachine(t, []string{"/dev/sda3"}, "findmnt", filepath.Join(dir, "g"), 1)
		rep, err := c.Check(context.Background(), "/etc/fstab", []byte("/dev/sda3 /data ext4 defaults 0 2\n"))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Findings
	}
	if got := brief(Added(run("one problem"), run("another problem"))); got != "1 fstab-verify blocker" {
		t.Errorf("got %q", got)
	}
	if got := brief(Added(run("one problem"), run("one problem"))); got != "" {
		t.Errorf("the same error: %q", got)
	}
}

// A findmnt that was killed, or printed more than sc reads, is said so.
func TestFstabFindmntBroken(t *testing.T) {
	c, _ := fakeMachine(t, nil, "", "", 0)
	fstab := []byte("/dev/sdz9 /data ext4 defaults 0 2\n")
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "findmnt"), []byte("#!/bin/sh\necho /data\necho '   [E] half a messa'\nkill -9 $$\n"), 0o755)
	rep, err := c.Check(context.Background(), "/etc/fstab", fstab)
	if err != nil || brief(rep.Findings) != "1 fstab-source-missing blocker" || strings.Join(rep.Notes, "|") != "findmnt was killed; only sc's own rules ran" {
		t.Errorf("killed: %q %q %v", brief(rep.Findings), rep.Notes, err)
	}
	os.WriteFile(filepath.Join(c.Run.Dirs[0], "findmnt"), []byte("#!/bin/sh\nhead -c 5000 /dev/zero | tr '\\0' x\n"), 0o755)
	c.Run.MaxOut = 1000
	rep, err = c.Check(context.Background(), "/etc/fstab", fstab)
	if err != nil || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "findmnt printed more than sc reads") {
		t.Errorf("cut: %q %v", rep.Notes, err)
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
	notes := strings.Join(rep.Notes, "|")
	if got := brief(rep.Findings); got != "1 fstab-source-missing blocker\n2 fstab-fields error" || (notes != "" && notes != noteUnread) {
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

// A word in mountopts.txt that is one letter from a common option hides
// that misspelling, so each such word is a real option, checked by hand.
func TestKnownOptsNearCommon(t *testing.T) {
	real := map[string]bool{"async": true, "sync": true, "user": true, "users": true,
		"notail": true, "nouuid": true, "_rnetdev": true, "wsync": true}
	for o := range knownOpts {
		for _, c := range commonOpts {
			if o != c && len(o) >= 4 && editDistance(o, c) == 1 && !real[o] {
				t.Errorf("mountopts.txt has %q, one letter from %q: a real option?", o, c)
			}
		}
	}
	if len(knownOpts) < 700 {
		t.Errorf("only %d known options", len(knownOpts))
	}
}

func TestOptionTypo(t *testing.T) {
	for o, want := range map[string]string{
		"defalts": "defaults", "defualts": "defaults", "default": "defaults", "nofial": "nofail", "noatme": "noatime",
		"nosiud": "nosuid", "usres": "users", "erors": "errors", "netdev": "_netdev",
		"nossd": "", "resuid": "", "wsync": "", "nocto": "", "_rnetdev": "", "data": "", "nr_inodes": "", "journal_checksum": "",
		"dafaluts": "", // two slips: too far to call
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

// A source that is a path outside /dev (a bind source, an image file, a
// swap file) must exist, with the line's severity; one below another
// line's mount point is not looked for, nor a pseudo or fuse source.
// x-systemd.automount makes a missing source an error: only the
// automount unit is required at boot.
func TestFstabPathSources(t *testing.T) {
	for _, tc := range []struct{ fstab, want string }{
		{"/srv/data /var/lib/docker none bind 0 0\n", ""},
		{"/srv/dat /var/lib/docker none bind 0 0\n", "1 fstab-source-missing blocker"},
		{"/srv/dat /mnt/x none rbind,nofail 0 0\n", "1 fstab-source-missing warning"},
		{"/srv/disk.img /mnt/img ext4 loop 0 2\n", "1 fstab-source-missing blocker"},
		{"/srv/disk.img /mnt/img ext4 defaults 0 2\n", "1 fstab-source-missing blocker"},
		{"/swap.img none swap sw 0 0\n", "1 fstab-source-missing error"},
		{"/swapfile none swap sw 0 0\n", ""},
		{"/dev/sdz9 /data ext4 x-systemd.automount 0 2\n", "1 fstab-source-missing error"},
		{"/dev/sda3 /mnt/new ext4 defaults 0 2\n/mnt/new/sub /srv/sub none bind 0 0\n", ""},
		{"tmpfs /tmp tmpfs defaults 0 0\noverlay /merged overlay lowerdir=/a,upperdir=/b 0 0\n", ""},
		{"/mnt/disk* /storage fuse.mergerfs defaults 0 0\n", ""},
	} {
		c, _ := fakeMachine(t, []string{"/dev/sda3", "/srv/data", "/swapfile"}, "", "", 0)
		rep, err := c.Check(context.Background(), "/etc/fstab", []byte(tc.fstab))
		if err != nil {
			t.Fatal(err)
		}
		if got := brief(rep.Findings); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.fstab, got, tc.want)
		}
	}
	c, _ := fakeMachine(t, nil, "", "", 0)
	rep, _ := c.Check(context.Background(), "/etc/fstab", []byte("/srv/dat /var/lib/docker none bind 0 0\n"))
	if len(rep.Findings) != 1 || rep.Findings[0].Text != "/srv/dat (bind source for /var/lib/docker) does not exist" {
		t.Errorf("%+v", rep.Findings)
	}
}

// Not root: a path sc may not look at is not missing.
func TestFstabPathSourceUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	dir := t.TempDir()
	shut := filepath.Join(dir, "shut")
	os.Mkdir(shut, 0o000)
	t.Cleanup(func() { os.Chmod(shut, 0o755) })
	c := &Checks{Home: t.TempDir()}
	if c.pathMissing(filepath.Join(shut, "img")) || !c.pathMissing(filepath.Join(dir, "img")) {
		t.Error("pathMissing")
	}
}

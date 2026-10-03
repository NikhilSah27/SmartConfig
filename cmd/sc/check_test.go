package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smartconfig/internal/check"
	"smartconfig/internal/scope"
)

// checkEnv gives sc check a graph whose fstab is dir/fstab and a machine
// with no validators, so only sc's own rules run. /dev/null is a device
// that exists everywhere.
func checkEnv(t *testing.T) (dir, fstab string) {
	t.Helper()
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	dir = t.TempDir()
	fstab = filepath.Join(dir, "fstab")
	g, err := check.ParseGraph("check fstab " + fstab + "\n")
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	recordedFile = func(p string) bool { return !strings.HasSuffix(p, "~") }
	t.Cleanup(func() {
		testHookChecks = nil
		recordedFile = func(p string) bool { return scope.Default().Recorded(p) }
	})
	return dir, fstab
}

const (
	noValidator = "no validator found (findmnt); only sc's own rules ran\n"
	badFstab    = "/dev/sc-no-such-disk /data ext4 defaults 0 2\n/dev/null /x ext4 defalts,nofail 0 2\n"
)

func TestCheckCLI(t *testing.T) {
	dir, fstab := checkEnv(t)

	// A clean file: exit 0.
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	if r := sc(t, "check", fstab); r.code != 0 || r.stderr != "" ||
		r.stdout != "note: "+fstab+": "+noValidator+"no problems found in 1 file\n" {
		t.Fatalf("clean: %+v", r)
	}

	// A blocker and a warning: the table, exit 2, nothing on stderr.
	os.WriteFile(fstab, []byte(badFstab), 0o644)
	r := sc(t, "check", fstab)
	lines := strings.Split(r.stdout, "\n")
	if r.code != 2 || r.stderr != "" || len(lines) != 7 {
		t.Fatalf("findings: %+v", r)
	}
	for i, want := range [][]string{
		{"SEVERITY", "FILE", "LINE", "RULE", "PROBLEM"},
		{"blocker", fstab, "1", "fstab-source-missing", "/dev/sc-no-such-disk (for /data) is not a device on this machine"},
		{"warning", fstab, "2", "fstab-option-typo", `option "defalts" of /x looks like a misspelling of "defaults"`},
	} {
		got := strings.Fields(lines[i])
		if strings.Join(got[:4], " ") != strings.Join(want[:4], " ") || !strings.HasSuffix(lines[i], want[4]) {
			t.Errorf("line %d: %q, want %q", i, lines[i], want)
		}
	}
	if lines[3] != "note: "+fstab+": "+strings.TrimSpace(noValidator) || lines[4] != "1 blocker, 1 warning in 1 file." ||
		lines[5] != "sc check -v explains; sc log FILE lists the versions to restore." {
		t.Errorf("tail: %q", lines[3:])
	}
	if len(lines[4]) > 80 || len(lines[5]) > 80 {
		t.Errorf("a summary line is wider than 80 columns: %q", lines[4:6])
	}

	// With no argument: the files of the graph that are on disk.
	if r2 := sc(t, "check"); r2.code != 2 || r2.stdout != r.stdout {
		t.Errorf("no argument: %+v", r2)
	}

	// -v explains each rule once and drops the hint.
	v := sc(t, "check", "-v", fstab)
	if v.code != 2 || !strings.Contains(v.stdout, "1 blocker, 1 warning in 1 file.\n\nfstab-source-missing:\n  The line names a disk or partition") ||
		!strings.Contains(v.stdout, "\nfstab-option-typo:\n  The option is not one sc knows") || strings.Contains(v.stdout, "sc check -v explains") {
		t.Errorf("-v: %+v", v)
	}

	// Warnings alone: exit 0.
	os.WriteFile(fstab, []byte("/dev/null /x ext4 defalts,nofail 0 2\n"), 0o644)
	if r := sc(t, "check", fstab); r.code != 0 || !strings.Contains(r.stdout, "1 warning in 1 file.") {
		t.Errorf("warning only: %+v", r)
	}

	// A file no checker reads; a path that is not there; a symlink.
	other := filepath.Join(dir, "other")
	os.WriteFile(other, []byte("x\n"), 0o644)
	if r := sc(t, "check", other); r.code != 0 || r.stdout != "note: "+other+": no checker reads this file\n" {
		t.Errorf("no checker: %+v", r)
	}
	if r := sc(t, "check", filepath.Join(dir, "missing")); r.code != 1 || r.stdout != "" || strings.Count(r.stderr, "\n") != 1 || !strings.HasPrefix(r.stderr, "sc: ") {
		t.Errorf("missing: %+v", r)
	}
	link := filepath.Join(dir, "link")
	os.Symlink(fstab, link)
	if r := sc(t, "check", link); r.code != 1 || !strings.Contains(r.stderr, "is a symlink; sc check reads files") {
		t.Errorf("symlink: %+v", r)
	}
}

// An id checks that saved version, whatever the file holds now; a file
// whose name looks like an id is given as ./name.
func TestCheckCLIByID(t *testing.T) {
	dir, fstab := checkEnv(t)
	mustSC(t, "init")
	os.WriteFile(fstab, []byte(badFstab), 0o644)
	id := strings.TrimSpace(mustSC(t, "snapshot", "-q", fstab))
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)

	r := sc(t, "check", id)
	if r.code != 2 || !strings.Contains(r.stdout, "blocker   "+fstab+" ("+id+")  1     fstab-source-missing") {
		t.Errorf("by id: %+v", r)
	}
	if r := sc(t, "check", fstab); r.code != 0 {
		t.Errorf("the file now: %+v", r)
	}
	// An id prefix and upper case work as for sc cat; an unknown id and
	// something too long to be an id are one-line errors.
	for _, a := range []string{id[:3], strings.ToUpper(id)} {
		if r := sc(t, "check", a); r.code != 2 || !strings.Contains(r.stdout, " ("+id+") ") {
			t.Errorf("id %q: %+v", a, r)
		}
	}
	t.Chdir(dir)
	for _, a := range []string{"abcdef", "deadbeef"} {
		if r := sc(t, "check", a); r.code != 1 || r.stdout != "" || !strings.HasPrefix(r.stderr, "sc: ") || strings.Count(r.stderr, "\n") != 1 {
			t.Errorf("%s: %+v", a, r)
		}
	}
	// A file that exists is a path, whatever its name looks like.
	os.WriteFile("cafe", []byte("x\n"), 0o644)
	if r := sc(t, "check", "cafe"); r.code != 0 || !strings.Contains(r.stdout, "cafe: no checker reads this file") {
		t.Errorf("a file named like an id: %+v", r)
	}
}

// Without the right to write $SC_HOME (a user, no sudo) sc check still
// checks a file the user can read, in a scratch directory of its own.
func TestCheckCLIWithoutStoreAccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write anywhere")
	}
	_, fstab := checkEnv(t)
	locked := filepath.Join(t.TempDir(), "locked")
	os.Mkdir(locked, 0o500)
	t.Setenv("SC_HOME", filepath.Join(locked, "home"))
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	os.WriteFile(fstab, []byte(badFstab), 0o644)
	if r := sc(t, "check", fstab); r.code != 2 || !strings.Contains(r.stdout, "fstab-source-missing") {
		t.Fatalf("%+v", r)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("scratch directory left behind: %v", left)
	}
}

// With no argument, a file sc has a checker for but may not read is not a
// clean file: exit 1 and one line on stderr, after the notes.
func TestCheckCLIUncheckedFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	_, fstab := checkEnv(t)
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o000)
	r := sc(t, "check")
	if r.code != 1 || r.stderr != "sc: 1 file not checked (see the notes)\n" ||
		r.stdout != "note: "+fstab+": not checked: permission denied (run sc check as root)\n" {
		t.Fatalf("%+v", r)
	}
}

// With no argument, sc check looks only at files sc keeps: an editor's
// backup next to a checked file is left out.
func TestCheckCLISkipsUnrecorded(t *testing.T) {
	dir, fstab := checkEnv(t)
	g, _ := check.ParseGraph("check fstab " + dir + "/fstab*\n")
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	os.WriteFile(fstab+"~", []byte(badFstab), 0o644)
	if r := sc(t, "check"); r.code != 0 || !strings.Contains(r.stdout, "no problems found in 1 file") {
		t.Fatalf("%+v", r)
	}
	// Named, it is checked.
	if r := sc(t, "check", fstab+"~"); r.code != 2 {
		t.Fatalf("named: %+v", r)
	}
	// The default scope leaves these names out.
	for _, p := range []string{"/etc/sudoers.d/90-local~", "/etc/sudoers.d/90-local.dpkg-old"} {
		if scope.Default().Recorded(p) {
			t.Errorf("the default scope records %s", p)
		}
	}
}

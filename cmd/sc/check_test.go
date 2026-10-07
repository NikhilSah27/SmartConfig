package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
	if v.code != 2 || !strings.Contains(v.stdout, "1 blocker, 1 warning in 1 file.\n\nfstab-source-missing:\n  The line names a disk, partition, image file") ||
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

// A validator cut short (here: out of time) leaves the file not fully
// checked: exit 1, never "no problems found".
func TestCheckCLIIncomplete(t *testing.T) {
	_, fstab := checkEnv(t)
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs, c.Run.Timeout = []string{tools}, 300*time.Millisecond }
	r := sc(t, "check", fstab)
	if r.code != 1 || r.stderr != "sc: 1 file not fully checked (see the notes)\n" || strings.Contains(r.stdout, "no problems found") ||
		!strings.Contains(r.stdout, "findmnt did not finish in time") {
		t.Fatalf("%+v", r)
	}
}

// A relative SC_HOME still gives the validator a path it can open (it
// once ran in the scratch directory with a relative path: a false clean).
func TestCheckCLIRelativeHome(t *testing.T) {
	dir, fstab := checkEnv(t)
	t.Chdir(dir)
	t.Setenv("SC_HOME", "home")
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nfor f; do :; done\n"+
		"[ -r \"$f\" ] || { echo \"findmnt: $f: No such file or directory\" >&2; exit 1; }\necho /data\necho '   [E] something new'\nexit 1\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
	if r := sc(t, "check", fstab); r.code != 2 || !strings.Contains(r.stdout, "fstab-verify") {
		t.Fatalf("%+v", r)
	}
}

// On a read-only root (the rescue shell) $SC_HOME and $TMPDIR are
// read-only too: the copies go to the next writable place (a tmpfs) and
// are removed; with none, one line names every place tried.
func TestCheckCLIScratchFallback(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through file modes")
	}
	_, fstab := checkEnv(t)
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	ro := func() string {
		d := t.TempDir()
		os.Chmod(d, 0o500)
		t.Cleanup(func() { os.Chmod(d, 0o700) })
		return d
	}
	home := ro()
	t.Setenv("SC_HOME", home)
	shm := t.TempDir()
	old := scratchParents
	t.Cleanup(func() { scratchParents = old })
	scratchParents = func() []string { return []string{ro(), shm} }
	if r := sc(t, "check", fstab); r.code != 0 || !strings.Contains(r.stdout, "no problems found in 1 file") {
		t.Fatalf("fallback: %+v", r)
	}
	if left, _ := os.ReadDir(shm); len(left) != 0 {
		t.Errorf("scratch left: %v", left)
	}
	scratchParents = func() []string { return []string{ro(), ro()} }
	if r := sc(t, "check", fstab); r.code != 1 || !strings.HasPrefix(r.stderr, "sc: no scratch directory for the validators' copies: "+home+"/tmp (permission denied); no writable place for a private directory: ") ||
		strings.Count(r.stderr, "permission denied") != 3 || strings.Count(r.stderr, "\n") != 1 {
		t.Errorf("none writable: %+v", r)
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

// One set of checks both picks the files and checks them: the graph is
// worked out once.
func TestCheckCLIOneChecks(t *testing.T) {
	_, fstab := checkEnv(t)
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	hook, n := testHookChecks, 0
	testHookChecks = func(c *check.Checks) { n++; hook(c) }
	for _, args := range [][]string{{"check"}, {"check", fstab}, {"check", "--as", fstab, fstab}} {
		n = 0
		if r := sc(t, args...); r.code != 0 || n != 1 {
			t.Errorf("%q: %d sets of checks: %+v", args, n, r)
		}
	}
}

// --as checks a file's content as if it were at the path given: the
// path's checker runs, the file at that path is not read, and rules about
// the file on disk do not apply.
func TestCheckCLIAs(t *testing.T) {
	dir, fstab := checkEnv(t)
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644) // what is there now: clean
	cand := filepath.Join(dir, "candidate")
	os.WriteFile(cand, []byte(badFstab), 0o644)
	r := sc(t, "check", "--as", fstab, cand)
	if r.code != 2 || !strings.Contains(r.stdout, "blocker   "+fstab+" (from "+cand+")  1     fstab-source-missing") {
		t.Fatalf("%+v", r)
	}
	// A relative path is taken from the working directory; so is the file.
	t.Chdir(dir)
	if r := sc(t, "check", "--as", "fstab", "candidate"); r.code != 2 || !strings.Contains(r.stdout, fstab+" (from candidate)") {
		t.Errorf("relative: %+v", r)
	}
	for _, args := range [][]string{
		{"check", "--as", fstab},
		{"check", "--as", fstab, cand, cand},
		{"check", "--as", fstab, filepath.Join(dir, "missing")},
		{"check", "--as", fstab, dir},
	} {
		if r := sc(t, args...); r.code != 1 || r.stdout != "" || !strings.HasPrefix(r.stderr, "sc: ") || strings.Count(r.stderr, "\n") != 1 {
			t.Errorf("%q: %+v", args, r)
		}
	}
	if r := sc(t, "check", "--as", filepath.Join(dir, "nochecker"), cand); r.code != 0 || !strings.Contains(r.stdout, "no checker reads this file") {
		t.Errorf("no checker: %+v", r)
	}
	// A FIFO with no writer is refused at once, not waited on.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan result, 1)
	go func() { done <- sc(t, "check", "--as", fstab, fifo) }()
	select {
	case r := <-done:
		if r.code != 1 || r.stderr != "sc: "+fifo+" is not a regular file\n" {
			t.Errorf("fifo: %+v", r)
		}
	case <-time.After(10 * time.Second):
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close() // lets the open return
		}
		t.Fatal("sc check --as waited on a FIFO")
	}
}

// A validator's words behind a note may quote a file, as a finding's raw
// lines may: sc check shows them with -v only, and says so without (M3
// follow-up 4).
func TestCheckCLISaid(t *testing.T) {
	_, fstab := checkEnv(t)
	os.WriteFile(fstab, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\necho \"findmnt: unrecognized option '--x'\" >&2\nexit 1\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
	note := "note: " + fstab + ": findmnt could not check the file; only sc's own rules ran\n"
	r := sc(t, "check", fstab)
	if !strings.HasPrefix(r.stdout, note+"sc check -v shows what the validators said.\n") || strings.Contains(r.stdout, "unrecognized") {
		t.Errorf("without -v: %+v", r)
	}
	r = sc(t, "check", "-v", fstab)
	if !strings.HasPrefix(r.stdout, note+"\nbehind the notes, from the validators:\n  "+fstab+": findmnt: unrecognized option '--x'\n") {
		t.Errorf("with -v: %+v", r)
	}
}

// A file whose check did nothing (a drop-in for every unit with a prefix)
// is a file not checked: exit 1, not "no problems found" (review of chunk
// F, B6). Its note, which carries the stem from the path, is escaped (B7).
func TestCheckCLIUnchecked(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Run.Dirs = []string{empty} }
	t.Cleanup(func() { testHookChecks = nil })
	cand := filepath.Join(t.TempDir(), "d.conf")
	os.WriteFile(cand, []byte("[Service]\nRestart=always\n"), 0o644)
	r := sc(t, "check", "--as", "/etc/systemd/system/x\x1b[7m-.service.d/a.conf", cand)
	if r.code != 1 || r.stderr != "sc: 1 file not checked (see the notes)\n" || strings.Contains(r.stdout, "no problems found") ||
		strings.Contains(r.stdout, "\x1b") || !strings.Contains(r.stdout, `a drop-in for every unit whose name starts with x\x1b[7m-; it was not checked`) {
		t.Errorf("%+v", r)
	}
}

// What a validator said behind a note is escaped as a finding's raw lines
// are; with findings, the "-v explains" line says enough, and the
// "behind the notes" block is set off from the tally.
func TestCheckCLISaidShown(t *testing.T) {
	_, fstab := checkEnv(t)
	os.WriteFile(fstab, []byte(badFstab), 0o644)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nprintf 'findmnt: \\033[7mbad\\n' >&2\nexit 1\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
	r := sc(t, "check", fstab)
	if strings.Contains(r.stdout, "shows what the validators said") || !strings.Contains(r.stdout, "sc check -v explains") {
		t.Errorf("without -v: %q", r.stdout)
	}
	r = sc(t, "check", "-v", fstab)
	if strings.Contains(r.stdout, "\x1b") || !strings.Contains(r.stdout, "  "+fstab+`: "findmnt: \x1b[7mbad"`+"\n\n1 blocker, 1 warning in 1 file.") {
		t.Errorf("with -v: %q", r.stdout)
	}
}

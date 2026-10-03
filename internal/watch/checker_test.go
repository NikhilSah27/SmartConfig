//go:build linux

package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// withChecks gives the watcher a graph whose fstab checker reads the files
// of root matching glob, and tool dir tools (nil: none, so only sc's own
// fstab rules run). /dev/null exists on every machine, /dev/sc-no-such-disk
// on none.
func withChecks(t *testing.T, e *env, glob string, tools string) {
	t.Helper()
	g, err := check.ParseGraph("check fstab " + filepath.Join(e.root, glob) + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if tools == "" {
		tools = t.TempDir()
	}
	e.cfg.Checks = &check.Checks{Home: e.home, Graph: g, Run: check.Runner{Dirs: []string{tools}}}
}

const (
	goodTab  = "/dev/null /data ext4 defaults 0 2\n"
	badTab   = "/dev/sc-no-such-disk /data ext4 defaults 0 2\n"
	worseTab = "/dev/sc-no-such-disk /data ext4 defaults 0 2\n/dev/null /x ext4 defalts,nofail 0 2\n"
)

// lines returns the log lines that contain s.
func (e *env) lines(s string) []string {
	var out []string
	for _, l := range strings.Split(e.log.String(), "\n") {
		if strings.Contains(l, s) {
			out = append(out, l)
		}
	}
	return out
}

// A change that adds a problem gets one journal line per new finding, at
// a priority from its severity; a problem already reported is not
// reported again; a file clean of what was reported is "ok again". The
// startup baseline is not checked.
func TestCheckAfterChange(t *testing.T) {
	e := newEnv(t)
	tab := filepath.Join(e.root, "fstab")
	put(t, tab, badTab, 0o644) // already bad at start: the baseline is no change
	withChecks(t, e, "fstab", "")
	e.start()
	e.barrier()
	if l := e.lines("check:"); len(l) != 0 {
		t.Fatalf("the baseline was checked: %q", l)
	}

	put(t, tab, goodTab, 0o644)
	e.waitFor("the good row", func() bool { return len(e.history(tab)) == 2 })
	put(t, tab, badTab, 0o644)
	e.waitFor("the blocker line", func() bool { return len(e.lines(": check: blocker")) == 1 })
	id := e.newest(tab).ID
	tier := fmt.Sprintf("T%d ", e.w.tierOf(tab))
	want := "<3>" + tier + tab + ": check: blocker fstab-source-missing, line 1: /dev/sc-no-such-disk (for /data) is not a device on this machine (" + id + ")"
	if got := e.lines(": check: blocker")[0]; got != want {
		t.Fatalf("line:\n got %q\nwant %q", got, want)
	}

	// The blocker is still there and a warning is new: only the warning.
	put(t, tab, worseTab, 0o644)
	e.waitFor("the warning line", func() bool { return len(e.lines(": check: warning")) == 1 })
	if l := e.lines(": check: warning")[0]; !strings.HasPrefix(l, "<5>"+tier+tab+": check: warning fstab-option-typo, line 2: ") {
		t.Fatalf("warning line %q", l)
	}
	if n := len(e.lines(": check: blocker")); n != 1 {
		t.Fatalf("the blocker was reported %d times", n)
	}

	// Fixed: ok again, once.
	put(t, tab, goodTab, 0o644)
	e.waitFor("ok again", func() bool { return len(e.lines(": check: ok again")) == 1 })
	if l := e.lines(": check: ok again")[0]; l != "<6>"+tier+tab+": check: ok again ("+e.newest(tab).ID+")" {
		t.Fatalf("ok line %q", l)
	}
	put(t, tab, "# a comment\n"+goodTab, 0o644)
	e.waitFor("the comment row", func() bool { return len(e.history(tab)) == 6 })
	e.barrier()
	if n := len(e.lines(": check: ok again")); n != 1 {
		t.Fatalf("ok again %d times", n)
	}
}

// A file with no checker, a deletion and a new file with no problem give
// no check line; the checker reads no validator output into the journal.
func TestCheckQuietCases(t *testing.T) {
	e := newEnv(t)
	tab := filepath.Join(e.root, "fstab")
	put(t, tab, goodTab, 0o644)
	withChecks(t, e, "fstab", "")
	var checked atomic.Int32
	testHookChecked = func(string) { checked.Add(1) }
	t.Cleanup(func() { testHookChecked = nil })
	e.start()
	put(t, filepath.Join(e.root, "other"), badTab, 0o644) // no checker
	os.Remove(tab)                                        // a deletion: no content
	e.waitFor("the deleted row", func() bool { return len(e.history(tab)) == 2 })
	e.barrier()
	if n := checked.Load(); n != 0 {
		t.Fatalf("%d checks", n)
	}
	put(t, tab, goodTab, 0o644) // created again: checked, nothing to say
	e.waitFor("the check", func() bool { return checked.Load() == 1 })
	if l := e.lines("check:"); len(l) != 0 {
		t.Fatalf("check lines: %q", l)
	}
}

// A slow validator does not hold up the worker: rows of other files are
// recorded while a check runs.
func TestCheckDoesNotHoldUpRows(t *testing.T) {
	e := newEnv(t)
	tools := t.TempDir()
	release := filepath.Join(tools, "release")
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nwhile [ ! -e "+release+" ]; do sleep 0.05; done\n"), 0o755)
	tab := filepath.Join(e.root, "fstab")
	put(t, tab, goodTab, 0o644)
	withChecks(t, e, "fstab", tools)
	e.cfg.Checks.Run.Timeout = time.Minute
	started := make(chan struct{}, 1)
	testHookBeforeCheck = func(string) { started <- struct{}{} }
	t.Cleanup(func() { testHookBeforeCheck = nil; os.WriteFile(release, nil, 0o644) })
	e.start()
	put(t, tab, badTab, 0o644)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the check did not start")
	}
	other := filepath.Join(e.root, "other")
	put(t, other, "x\n", 0o644)
	e.waitFor("a row while the check runs", func() bool { return len(e.history(other)) > 0 })
	if l := e.lines("check:"); len(l) != 0 {
		t.Fatalf("the check finished first: %q", l)
	}
	os.WriteFile(release, nil, 0o644)
	e.waitForWithin(30*time.Second, "the check", func() bool { return len(e.lines(": check: blocker")) == 1 })
}

// The queue holds CheckQueue paths; a path that does not fit is dropped
// with one line until the queue takes one again, and a path already
// waiting takes its new change in place.
func TestCheckQueueBound(t *testing.T) {
	e := newEnv(t)
	tools := t.TempDir()
	release := filepath.Join(tools, "release")
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nwhile [ ! -e "+release+" ]; do sleep 0.05; done\n"), 0o755)
	tabs := []string{filepath.Join(e.root, "fstab1"), filepath.Join(e.root, "fstab2"), filepath.Join(e.root, "fstab3"), filepath.Join(e.root, "fstab4")}
	for _, p := range tabs {
		put(t, p, goodTab, 0o644)
	}
	withChecks(t, e, "fstab*", tools)
	e.cfg.Checks.Run.Timeout = time.Minute
	e.cfg.CheckQueue = 1
	started := make(chan string, 10)
	testHookBeforeCheck = func(p string) { started <- p }
	t.Cleanup(func() { testHookBeforeCheck = nil; os.WriteFile(release, nil, 0o644) })
	e.start()
	put(t, tabs[0], badTab, 0o644) // taken by the checker, which then waits
	if p := <-started; p != tabs[0] {
		t.Fatalf("first check of %s", p)
	}
	put(t, tabs[1], badTab, 0o644) // waits in the queue
	e.waitFor("fstab2's row", func() bool { return len(e.history(tabs[1])) == 2 })
	put(t, tabs[1], worseTab, 0o644) // the same path: no new place needed
	e.waitFor("fstab2's second row", func() bool { return len(e.history(tabs[1])) == 3 })
	put(t, tabs[2], badTab, 0o644) // no room
	e.waitFor("the full line", func() bool { return len(e.lines("check queue full")) == 1 })
	if l := e.lines("check queue full")[0]; l != "<4>check queue full: "+tabs[2]+" was not checked (sc check "+tabs[2]+")" {
		t.Fatalf("line %q", l)
	}
	put(t, tabs[3], badTab, 0o644) // no room either: counted, not named
	e.waitFor("fstab4's row", func() bool { return len(e.history(tabs[3])) == 2 })
	os.WriteFile(release, nil, 0o644)
	e.waitForWithin(30*time.Second, "both checks", func() bool {
		return len(e.lines(tabs[0]+": check: blocker")) == 1 && len(e.lines(tabs[1]+": check:")) == 2
	})
	// fstab2 was compared with the content before its first queued change.
	if n := len(e.lines(tabs[1] + ": check: blocker")); n != 1 {
		t.Errorf("fstab2: %q", e.lines(tabs[1]))
	}
	if n := len(e.lines(tabs[2] + ": check:")); n != 0 {
		t.Errorf("fstab3 was checked: %q", e.lines(tabs[2]))
	}
	// The next path the queue takes: how many went unchecked.
	put(t, tabs[0], goodTab, 0o644)
	e.waitFor("the count line", func() bool { return len(e.lines("check queue full: 2 changed files were not checked in all")) == 1 })
}

// The version before is not checked again when the last check was of it,
// so a change runs the validator once; and a check that fails on the
// version before still checks the new one and reports what it finds.
func TestCheckRunsOnce(t *testing.T) {
	e := newEnv(t)
	tools := t.TempDir()
	runs := filepath.Join(tools, "runs")
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\necho x >> "+runs+"\n"), 0o755)
	tab := filepath.Join(e.root, "fstab")
	put(t, tab, goodTab, 0o644)
	withChecks(t, e, "fstab", tools)
	var checked atomic.Int32
	testHookChecked = func(string) { checked.Add(1) }
	t.Cleanup(func() { testHookChecked = nil })
	e.start()
	count := func() int { b, _ := os.ReadFile(runs); return strings.Count(string(b), "x") }
	put(t, tab, badTab, 0o644) // the first check: the version before and this one
	e.waitFor("the first check", func() bool { return checked.Load() == 1 })
	if n := count(); n != 2 {
		t.Fatalf("first check: %d validator runs, want 2", n)
	}
	put(t, tab, goodTab, 0o644) // the version before was the last one checked
	e.waitFor("the second check", func() bool { return checked.Load() == 2 })
	if n := count(); n != 3 {
		t.Fatalf("second check: %d validator runs in all, want 3", n)
	}
	if len(e.lines(": check: ok again")) != 1 {
		t.Fatalf("log:\n%s", e.log.String())
	}
}

// A restore or sc edit that fixes a reported file gets "ok again" with
// its own id, and a later unrelated change says nothing; a rescan does not
// check the deliberate row again; restoring a broken version is
// deliberate and logs nothing; a re-break right after a restore is
// reported (found by the M3 final review).
func TestCheckRestoreOkAgain(t *testing.T) {
	e := newEnv(t)
	tab := filepath.Join(e.root, "fstab")
	put(t, tab, goodTab, 0o644)
	withChecks(t, e, "fstab", "")
	var checked atomic.Int32
	testHookChecked = func(string) { checked.Add(1) }
	t.Cleanup(func() { testHookChecked = nil })
	e.start()
	good := e.newest(tab).ID
	put(t, tab, badTab, 0o644)
	e.waitFor("blocker", func() bool { return len(e.lines(": check: blocker")) == 1 })
	bad := e.newest(tab).ID
	r, _, err := e.st.Restore(good)
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("ok again at the restore", func() bool { return len(e.lines("ok again ("+r.ID+")")) == 1 })
	n := checked.Load()
	put(t, tab, "# unrelated\n"+goodTab, 0o644)
	e.waitFor("check", func() bool { return checked.Load() == n+1 })
	if l := e.lines("ok again"); len(l) != 1 {
		t.Fatalf("ok again: %q", l)
	}
	// An hourly rescan does not check the restore row again.
	put(t, tab, badTab, 0o644)
	e.waitFor("blocker 2", func() bool { return len(e.lines(": check: blocker")) == 2 })
	st, _ := fsutil.ReadState(tab)
	ed, _, err := e.st.Replace(tab, []byte(goodTab), 0o644, os.Getuid(), os.Getgid(), &st, store.OriginEdit, "sc edit")
	if err != nil {
		t.Fatal(err)
	}
	e.waitFor("ok again at the edit", func() bool { return len(e.lines("ok again ("+ed.ID+")")) == 1 })
	n = checked.Load()
	e.w.requestRescan()
	e.barrier()
	time.Sleep(200 * time.Millisecond)
	if c := checked.Load(); c != n {
		t.Fatalf("rescan checked again: %d -> %d", n, c)
	}
	// Restoring a broken version: deliberate, no line.
	if _, _, err := e.st.Restore(bad); err != nil {
		t.Fatal(err)
	}
	e.waitFor("check of the restore", func() bool { return checked.Load() == n+1 })
	if l := e.lines(": check: blocker"); len(l) != 2 {
		t.Fatalf("blocker after restoring a broken version: %q", l)
	}
	// Restore good, then re-break at once (merged into one job or not):
	// the re-break is reported.
	if _, _, err := e.st.Restore(good); err != nil {
		t.Fatal(err)
	}
	put(t, tab, badTab+"# again\n", 0o644)
	e.waitFor("blocker 3", func() bool { return len(e.lines(": check: blocker")) == 3 })
}

// A validator that does not finish says nothing about a problem it found
// before: no "ok again" for a file still broken, and the problem is not
// blamed again on the next change once the validator is back (found by
// the M3 final review: a timeout read as a clean run).
func TestCheckIncomplete(t *testing.T) {
	e := newEnv(t)
	tools := t.TempDir()
	slow := filepath.Join(tools, "slow")
	os.WriteFile(filepath.Join(tools, "sysctl"), []byte("#!/bin/sh\nif [ -e "+slow+" ]; then sleep 30; fi\n"+
		"for f; do :; done\nif grep -q '^foo.bar' \"$f\"; then echo 'sysctl: cannot stat /proc/sys/foo/bar: No such file or directory' >&2; exit 1; fi\n"), 0o755)
	conf := filepath.Join(e.root, "99-x.conf")
	put(t, conf, "vm.swappiness = 10\n", 0o644)
	g, err := check.ParseGraph("check sysctl " + filepath.Join(e.root, "*.conf") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	e.cfg.Checks = &check.Checks{Home: e.home, Graph: g, Run: check.Runner{Dirs: []string{tools}, Timeout: time.Second}}
	var checked atomic.Int32
	testHookChecked = func(string) { checked.Add(1) }
	t.Cleanup(func() { testHookChecked = nil })
	e.start()
	put(t, conf, "vm.swappiness = 10\nfoo.bar = 1\n", 0o644)
	e.waitFor("the warning", func() bool { return len(e.lines(": check: warning sysctl-unknown-key")) == 1 })
	os.WriteFile(slow, nil, 0o644)
	put(t, conf, "vm.swappiness = 10\nfoo.bar = 1\n# still bad\n", 0o644)
	e.waitForWithin(30*time.Second, "the timed-out check", func() bool { return checked.Load() == 2 })
	if l := e.lines("ok again"); len(l) != 0 {
		t.Fatalf("ok again for a file still broken: %q", l)
	}
	if l := e.lines(": a validator did not finish; the check is incomplete"); len(l) != 1 {
		t.Fatalf("incomplete line: %q", e.lines("check:"))
	}
	os.Remove(slow)
	put(t, conf, "vm.swappiness = 10\nfoo.bar = 1\n# still bad 2\n", 0o644)
	e.waitForWithin(30*time.Second, "the third check", func() bool { return checked.Load() == 3 })
	if l := e.lines(": check: warning"); len(l) != 1 {
		t.Fatalf("the old problem was blamed again: %q", e.lines("check:"))
	}
	put(t, conf, "vm.swappiness = 10\n", 0o644)
	e.waitFor("ok again", func() bool { return len(e.lines(": check: ok again")) == 1 })
}

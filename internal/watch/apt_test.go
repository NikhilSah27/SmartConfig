//go:build linux

package watch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/aptrun"
	"smartconfig/internal/store"
)

// A change scd sees while a dpkg run's apt is alive is tagged origin apt,
// its intent computed as ever and naming the run; with no mark, or a
// stale one, it is auto (M5 follow-ups, step 8). This test's own process
// stands in for apt.
func TestAptRunTagsRows(t *testing.T) {
	e := newEnv(t)
	mark := filepath.Join(t.TempDir(), "apt-run")
	e.cfg.AptRun = mark
	e.start()
	_, _, start, err := aptrun.Stat(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	begun := time.Date(2026, 10, 16, 7, 5, 12, 0, time.UTC)
	p := filepath.Join(e.root, "a.conf")
	step := func(m *aptrun.Mark, data, origin, intent string) {
		t.Helper()
		os.Remove(mark)
		if m != nil {
			os.WriteFile(mark, m.Encode(), 0o644)
		}
		n := len(e.history(p))
		put(t, p, data, 0o644)
		e.waitFor("a row for "+data, func() bool { return len(e.history(p)) > n })
		if c := e.newest(p); c.Origin != origin || c.Intent != intent {
			t.Errorf("%s: origin %q, intent %q; want %q, %q", data, c.Origin, c.Intent, origin, intent)
		}
	}
	step(&aptrun.Mark{PID: os.Getpid(), Start: start, Begun: begun}, "one\n", store.OriginApt, "created (apt run 2026-10-16 07:05:12 UTC)")
	step(&aptrun.Mark{PID: os.Getpid(), Start: start, Begun: begun}, "two\n", store.OriginApt, "changed (apt run 2026-10-16 07:05:12 UTC)")
	step(nil, "three\n", store.OriginAuto, "changed")
	step(&aptrun.Mark{PID: os.Getpid(), Start: start + 1, Begun: begun}, "four\n", store.OriginAuto, "changed") // a reused pid
	// sc apt begin writes a file: a symlink there is no mark (scd would
	// not see the file it points to change).
	good := filepath.Join(t.TempDir(), "good")
	os.WriteFile(good, aptrun.Mark{PID: os.Getpid(), Start: start, Begun: begun}.Encode(), 0o644)
	os.Remove(mark)
	os.Symlink(good, mark)
	n := len(e.history(p))
	put(t, p, "five\n", 0o644)
	e.waitFor("a row for five", func() bool { return len(e.history(p)) > n })
	if c := e.newest(p); c.Origin != store.OriginAuto {
		t.Errorf("a symlinked mark: origin %q, intent %q", c.Origin, c.Intent)
	}
}

// A change a dpkg run made is checked as any change scd saw: what it adds
// is reported, not left out as a deliberate restore or edit is, and a
// later change is judged against it.
func TestAptRunChecked(t *testing.T) {
	e := newEnv(t)
	tab := filepath.Join(e.root, "fstab")
	put(t, tab, goodTab, 0o644)
	withChecks(t, e, "fstab", "")
	mark := filepath.Join(t.TempDir(), "apt-run")
	e.cfg.AptRun = mark
	e.start()
	_, _, start, err := aptrun.Stat(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(mark, aptrun.Mark{PID: os.Getpid(), Start: start, Begun: time.Now()}.Encode(), 0o644)
	put(t, tab, badTab, 0o644)
	e.waitFor("the blocker line", func() bool { return len(e.lines(": check: blocker")) == 1 })
	if c := e.newest(tab); c.Origin != store.OriginApt {
		t.Fatalf("origin %q", c.Origin)
	}
	put(t, tab, worseTab, 0o644)
	e.waitFor("the warning line", func() bool { return len(e.lines(": check: warning")) == 1 })
	os.Remove(mark)
	put(t, tab, goodTab, 0o644)
	e.waitFor("ok again", func() bool { return len(e.lines(": check: ok again")) == 1 })
	if n := len(e.lines(": check: blocker")); n != 1 {
		t.Errorf("the blocker was reported %d times", n)
	}
}

// ourMark is a mark naming this test's process as the run's apt.
func ourMark(t *testing.T, begun time.Time) []byte {
	t.Helper()
	_, _, start, err := aptrun.Stat(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return aptrun.Mark{PID: os.Getpid(), Start: start, Begun: begun}.Encode()
}

// newAptEnv is a watcher whose rows wait 400 ms, so the tests below can
// begin or end a run in the quiet time, as apt does (the chunk L review,
// L2).
func newAptEnv(t *testing.T) (*env, string) {
	e := newEnv(t)
	e.cfg.Quiet = 400 * time.Millisecond
	e.cfg.AptRun = filepath.Join(t.TempDir(), "apt-run")
	e.start()
	return e, e.cfg.AptRun
}

// rowOf changes p by do and returns its new row.
func rowOf(e *env, p string, do func()) store.Change {
	e.t.Helper()
	n := len(e.history(p))
	do()
	e.waitFor("a row of "+p, func() bool { return len(e.history(p)) > n })
	return e.newest(p)
}

// dpkg's last change of a run comes ms before Post-Invoke's sc apt end,
// and scd records it after the quiet time: it is the run's all the same.
func TestAptChangeJustBeforeEnd(t *testing.T) {
	e, mark := newAptEnv(t)
	p := filepath.Join(e.root, "grub.cfg")
	c := rowOf(e, p, func() {
		os.WriteFile(mark, ourMark(t, time.Now()), 0o644)
		put(t, p, "dpkg\n", 0o644)
		time.Sleep(20 * time.Millisecond)
		os.Remove(mark)
	})
	if c.Origin != store.OriginApt {
		t.Errorf("a change made in the run: origin %q, intent %q", c.Origin, c.Intent)
	}
}

// So is a directory dpkg moved away just before the end: the rows of the
// files that were in it.
func TestAptDirMovedJustBeforeEnd(t *testing.T) {
	e, mark := newAptEnv(t)
	d := filepath.Join(e.root, "d")
	os.Mkdir(d, 0o755)
	p := filepath.Join(d, "a.conf")
	c := rowOf(e, p, func() { put(t, p, "one\n", 0o644) })
	if c.Origin != store.OriginAuto {
		t.Fatalf("before the run: origin %q", c.Origin)
	}
	c = rowOf(e, p, func() {
		os.WriteFile(mark, ourMark(t, time.Now()), 0o644)
		os.Rename(d, filepath.Join(t.TempDir(), "d"))
		time.Sleep(20 * time.Millisecond)
		os.Remove(mark)
	})
	if c.Kind != "deleted" || c.Origin != store.OriginApt {
		t.Errorf("a file moved away in the run: kind %q, origin %q, intent %q", c.Kind, c.Origin, c.Intent)
	}
}

// An edit of the owner's saved just before apt starts is the owner's.
func TestAptEditJustBeforeBegin(t *testing.T) {
	e, mark := newAptEnv(t)
	p := filepath.Join(e.root, "sshd_config")
	c := rowOf(e, p, func() {
		put(t, p, "owner\n", 0o644)
		time.Sleep(20 * time.Millisecond)
		os.WriteFile(mark, ourMark(t, time.Now()), 0o644)
	})
	if c.Origin != store.OriginAuto {
		t.Errorf("an edit made before the run: origin %q, intent %q", c.Origin, c.Intent)
	}
}

// Two changes in one quiet time make one row, the run's if the latest
// was made in the run: an edit of the owner's that dpkg then rewrites is
// dpkg's, and dpkg's change the owner edits after the run is the owner's.
func TestAptLatestEventWins(t *testing.T) {
	e, mark := newAptEnv(t)
	p := filepath.Join(e.root, "a.conf")
	c := rowOf(e, p, func() {
		put(t, p, "owner\n", 0o644)
		time.Sleep(20 * time.Millisecond)
		os.WriteFile(mark, ourMark(t, time.Now()), 0o644)
		put(t, p, "dpkg\n", 0o644)
	})
	if c.Origin != store.OriginApt {
		t.Errorf("dpkg's change after the owner's: origin %q, intent %q", c.Origin, c.Intent)
	}
	c = rowOf(e, p, func() {
		put(t, p, "dpkg 2\n", 0o644)
		time.Sleep(20 * time.Millisecond)
		os.Remove(mark)
		put(t, p, "owner 2\n", 0o644)
	})
	if c.Origin != store.OriginAuto {
		t.Errorf("the owner's edit after the run: origin %q, intent %q", c.Origin, c.Intent)
	}
	if got := e.history(p); len(got) != 3 { // its absence, then one per quiet time
		t.Errorf("rows %q", got)
	}
}

// A run whose apt dies with no sc apt end tags nothing soon after: scd
// looks its apt up again within aptLiveEvery.
func TestAptRunAptDies(t *testing.T) {
	e := newEnv(t)
	mark := filepath.Join(t.TempDir(), "apt-run")
	e.cfg.AptRun = mark
	e.start()
	apt := exec.Command("sleep", "60")
	if err := apt.Start(); err != nil {
		t.Fatal(err)
	}
	_, _, start, err := aptrun.Stat(apt.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(mark, aptrun.Mark{PID: apt.Process.Pid, Start: start, Begun: time.Now()}.Encode(), 0o644)
	p := filepath.Join(e.root, "a.conf")
	if c := rowOf(e, p, func() { put(t, p, "one\n", 0o644) }); c.Origin != store.OriginApt {
		t.Fatalf("in the run: origin %q", c.Origin)
	}
	apt.Process.Kill()
	apt.Wait()
	time.Sleep(2 * aptLiveEvery)
	if c := rowOf(e, p, func() { put(t, p, "two\n", 0o644) }); c.Origin != store.OriginAuto {
		t.Errorf("after apt died: origin %q, intent %q", c.Origin, c.Intent)
	}
}

// scd restarted in a run (the package's postinst restarts it) rescans: a
// file that changed while it was down is the run's only if it changed
// after the run began, as the time of its mark's file says (the chunk L
// review, L4). A file time outside the second of the mark's Begun is
// not sc apt begin's: Begun is taken then.
func TestAptStartupRescan(t *testing.T) {
	for _, tc := range []struct {
		name  string
		begin func(mark, p string) // a run begins, p changes
		want  string
	}{
		{"edit before the run", func(mark, p string) {
			put(t, p, "owner\n", 0o644)
			os.WriteFile(mark, ourMark(t, time.Now()), 0o644)
		}, store.OriginAuto},
		{"change in the run", func(mark, p string) {
			os.WriteFile(mark, ourMark(t, time.Now()), 0o644)
			time.Sleep(20 * time.Millisecond) // file times tick every few ms
			put(t, p, "dpkg\n", 0o644)
		}, store.OriginApt},
		{"a run that begins later", func(mark, p string) {
			os.WriteFile(mark, ourMark(t, time.Now().Add(time.Hour)), 0o644)
			time.Sleep(20 * time.Millisecond)
			put(t, p, "owner\n", 0o644)
		}, store.OriginAuto},
		{"a run that began earlier", func(mark, p string) {
			put(t, p, "dpkg\n", 0o644)
			time.Sleep(20 * time.Millisecond)
			os.WriteFile(mark, ourMark(t, time.Now().Add(-time.Hour)), 0o644)
		}, store.OriginApt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			mark := filepath.Join(t.TempDir(), "apt-run")
			e.cfg.AptRun = mark
			p := filepath.Join(e.root, "sshd_config")
			put(t, p, "one\n", 0o644)
			e.start()
			e.waitFor("the first row", func() bool { return len(e.history(p)) == 1 })
			e.stop()
			tc.begin(mark, p)
			e.start()
			e.waitFor("a row", func() bool { return len(e.history(p)) > 1 })
			c := e.newest(p)
			if c.Origin != tc.want || !strings.HasPrefix(c.Intent, "changed while not watching") {
				t.Errorf("origin %q, intent %q; want %q", c.Origin, c.Intent, tc.want)
			}
		})
	}
}

// In the check queue an apt row is no deliberate row: an auto row queued
// after it keeps it as the first row, so the check compares with the
// content before the run's change.
func TestAptRowQueued(t *testing.T) {
	q := newCheckQueue(10)
	q.add(store.Change{ID: "a1", Path: "/p", Origin: store.OriginApt})
	q.add(store.Change{ID: "a2", Path: "/p", Origin: store.OriginAuto})
	if j := q.jobs["/p"]; j.first != "a1" || j.last != "a2" || j.deliberate {
		t.Errorf("job %+v", *j)
	}
}

//go:build linux

package watch

import (
	"os"
	"path/filepath"
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

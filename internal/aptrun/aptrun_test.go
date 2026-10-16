package aptrun

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeProc points procDir at a directory of made-up processes: pid ->
// (name, parent, start time).
func fakeProc(t *testing.T, ps map[int][3]string) {
	t.Helper()
	dir := t.TempDir()
	for pid, p := range ps {
		d := filepath.Join(dir, fmt.Sprint(pid))
		os.MkdirAll(d, 0o755)
		// Fields 3 to 22: state, ppid, then 17 zeros, then the start time.
		stat := fmt.Sprintf("%d (%s) S %s 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 %s 0 0\n", pid, p[0], p[1], p[2])
		os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644)
	}
	old := procDir
	procDir = dir
	t.Cleanup(func() { procDir = old })
}

func TestStat(t *testing.T) {
	fakeProc(t, map[int][3]string{40: {"apt (x) y", "1", "777"}})
	comm, ppid, start, err := Stat(40)
	if err != nil || comm != "apt (x) y" || ppid != 1 || start != 777 {
		t.Errorf("%q %d %d %v", comm, ppid, start, err)
	}
	if _, _, _, err := Stat(41); err == nil {
		t.Error("a missing process")
	}
	// The real one: this test's own process.
	procDir = "/proc"
	if comm, _, start, err := Stat(os.Getpid()); err != nil || comm == "" || start == 0 {
		t.Errorf("self: %q %d %v", comm, start, err)
	}
}

// Runner climbs past the shells apt runs the hook through, to apt.
func TestRunner(t *testing.T) {
	fakeProc(t, map[int][3]string{
		10: {"apt", "1", "500"}, 11: {"sh", "10", "501"}, 12: {"bash", "11", "502"},
		20: {"unattended-upgr", "1", "600"},
		30: {"sh", "1", "700"},
	})
	for parent, want := range map[int]int{12: 10, 11: 10, 10: 10, 20: 20, 30: 30} {
		pid, _, err := Runner(parent)
		if err != nil || pid != want {
			t.Errorf("Runner(%d) = %d, %v; want %d", parent, pid, err, want)
		}
	}
	if pid, start, _ := Runner(12); pid != 10 || start != 500 {
		t.Errorf("start %d", start)
	}
	if _, _, err := Runner(99); err == nil {
		t.Error("a missing parent")
	}
}

// A mark is the run while its apt lives with the same start time; a
// stale one (apt gone, or its pid reused) is no run.
func TestReadLive(t *testing.T) {
	fakeProc(t, map[int][3]string{10: {"apt", "1", "500"}, 11: {"cron", "1", "999"}})
	file := filepath.Join(t.TempDir(), "apt-run")
	// The name is in UTC whatever the machine's zone.
	loc := time.Local
	time.Local = time.FixedZone("IST", 5*3600+1800)
	t.Cleanup(func() { time.Local = loc })
	begun := time.Date(2026, 10, 16, 7, 5, 12, 0, time.UTC)
	for m, want := range map[Mark]bool{
		{PID: 10, Start: 500, Begun: begun}: true,
		{PID: 10, Start: 501, Begun: begun}: false, // the pid was reused
		{PID: 11, Start: 500, Begun: begun}: false,
		{PID: 12, Start: 500, Begun: begun}: false, // gone
	} {
		os.WriteFile(file, m.Encode(), 0o644)
		got, ok := Read(file)
		if ok != want || ok && (got.PID != m.PID || got.Name() != "2026-10-16 07:05:12 UTC") {
			t.Errorf("Read(%+v) = %+v, %v", m, got, ok)
		}
	}
	for _, bad := range []string{"", "10 500\n", "x 500 1\n", "0 500 1\n", "10 500 1 2\n"} {
		os.WriteFile(file, []byte(bad), 0o644)
		if _, ok := Read(file); ok {
			t.Errorf("Read(%q) ok", bad)
		}
	}
	os.Remove(file)
	if _, ok := Read(file); ok {
		t.Error("no file")
	}
}

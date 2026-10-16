package aptrun

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// Runner climbs past the shells apt runs the hook through, and past apt's
// fork for its hooks (RunScripts), to the apt that runs dpkg (the chunk L
// review, L1); one fork step only.
func TestRunner(t *testing.T) {
	fakeProc(t, map[int][3]string{
		10: {"apt", "1", "500"}, 11: {"sh", "10", "501"}, 12: {"bash", "11", "502"},
		20: {"unattended-upgr", "1", "600"},
		30: {"sh", "1", "700"},
		// apt-get, its fork for the hooks, sh -c, and above apt-get a
		// shell, then a daemon forked from a daemon.
		40: {"apt-get", "45", "800"}, 41: {"apt-get", "40", "801"}, 42: {"sh", "41", "802"}, 45: {"bash", "1", "790"},
		50: {"packagekitd", "1", "900"}, 51: {"packagekitd", "50", "901"}, 52: {"packagekitd", "51", "902"}, 53: {"sh", "52", "903"},
	})
	for parent, want := range map[int]int{12: 10, 11: 10, 10: 10, 20: 20, 30: 30, 42: 40, 41: 40, 53: 51} {
		pid, _, err := Runner(parent)
		if err != nil || pid != want {
			t.Errorf("Runner(%d) = %d, %v; want %d", parent, pid, err, want)
		}
	}
	if pid, start, _ := Runner(12); pid != 10 || start != 500 {
		t.Errorf("start %d", start)
	}
	if pid, start, _ := Runner(42); pid != 40 || start != 800 {
		t.Errorf("the fork's apt: %d, start %d", pid, start)
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

// Runner with real processes (the chunk L review, L1): python3 stands in
// for apt and does what
// apt's RunScripts does (fork; the child runs the hook line with system(),
// then _exit; the parent waits), then, as dpkg would run, asks whether
// the mark sc apt begin wrote is live. This test binary, run again by the
// hook line, is sc apt begin's Runner.
func TestRunnerUnderAptsRunScripts(t *testing.T) {
	if out := os.Getenv("SC_RUNNER_OUT"); out != "" {
		pid, start, err := Runner(os.Getppid())
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(out, Mark{PID: pid, Start: start, Begun: time.Now()}.Encode(), 0o644)
		return
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	mark := filepath.Join(t.TempDir(), "apt-run")
	line := fmt.Sprintf("if [ -x %s ]; then SC_RUNNER_OUT=%s %s -test.run='^TestRunnerUnderAptsRunScripts$' >/dev/null || true; fi", os.Args[0], mark, os.Args[0])
	script := `
import os, sys
pid = os.fork()
if pid == 0:
    rc = os.system(sys.argv[1])
    os._exit(0 if rc == 0 else 101)
os.waitpid(pid, 0)
print(os.getpid())
`
	out, err := exec.Command(py, "-c", script, line).Output()
	if err != nil {
		t.Fatal(err)
	}
	apt, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	b, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	// The python process ("apt") has exited by now too, so liveness is
	// not asked here: only which process the mark names.
	if m.PID != apt {
		t.Errorf("the mark names pid %d; apt (the process that goes on to run dpkg) is %d", m.PID, apt)
	}
}

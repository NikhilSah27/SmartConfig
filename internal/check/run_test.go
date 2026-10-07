//go:build linux

package check

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tool writes an executable sh script named name into a fresh directory
// and returns a Runner that looks only there.
func tool(t *testing.T, name, script string) Runner {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Runner{Dirs: []string{dir}}
}

// The tool gets exactly the arguments, a fixed environment with nothing of
// the caller's, the given directory and an empty stdin; its exit status and
// output come back.
func TestRunExact(t *testing.T) {
	t.Setenv("SC_TEST_SECRET", "leak")
	r := tool(t, "fake", `for a in "$@"; do echo "arg:$a"; done
env | sort
pwd
cat
echo "to stderr" >&2
exit 3
`)
	dir := t.TempDir()
	res, err := r.Run(context.Background(), dir, "fake", "-f", "a b", "$(id)", "")
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Out)
	if !res.Found || res.Exit != 3 || res.TimedOut || res.Truncated {
		t.Fatalf("%+v\n%s", res, out)
	}
	if string(res.Err) != "to stderr\n" || strings.Contains(out, "to stderr") {
		t.Errorf("stderr %q; stdout:\n%s", res.Err, out)
	}
	for _, want := range []string{"arg:-f\narg:a b\narg:$(id)\narg:\n", "LC_ALL=C\n", "PATH=/usr/sbin:/usr/bin:/sbin:/bin\n", dir + "\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SC_TEST_SECRET") || strings.Contains(out, "HOME=") {
		t.Errorf("the caller's environment leaked:\n%s", out)
	}
}

// A tool that is not in the runner's directories is not found, even when
// $PATH has it; a name with a slash is refused.
func TestRunNotFound(t *testing.T) {
	r := tool(t, "fake", "exit 0\n")
	t.Setenv("PATH", r.Dirs[0]+":"+os.Getenv("PATH"))
	res, err := Runner{Dirs: []string{t.TempDir()}}.Run(context.Background(), "", "fake")
	if err != nil || res.Found {
		t.Fatalf("%+v %v", res, err)
	}
	os.WriteFile(filepath.Join(r.Dirs[0], "noexec"), []byte("#!/bin/sh\n"), 0o644)
	if res, err := r.Run(context.Background(), "", "noexec"); err != nil || res.Found {
		t.Fatalf("not executable: %+v %v", res, err)
	}
	for _, name := range []string{"", "../fake", "/bin/true", "a/b", "/usr/libexec/netplan/fake", "/usr/libexec/netplan/../netplan/generate"} {
		if _, err := r.Run(context.Background(), "", name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

// A tool of fullPathTools is named by its full path; in a test, Dirs
// stands in for its directory. (The real netplan generator is never run
// here without --root-dir: as root it would rewrite /run.)
func TestRunFullPath(t *testing.T) {
	r := tool(t, "generate", "echo \"ran $1\"\n")
	res, err := r.Run(context.Background(), "", "/usr/libexec/netplan/generate", "--root-dir")
	if err != nil || !res.Found || string(res.Out) != "ran --root-dir\n" {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err := r.Run(context.Background(), "", "generate"); err != nil || !res.Found {
		t.Fatalf("by name: %+v %v", res, err)
	}
	res, err = Runner{Dirs: []string{t.TempDir()}}.Run(context.Background(), "", "/usr/libexec/netplan/generate")
	if err != nil || res.Found {
		t.Fatalf("not in Dirs: %+v %v", res, err)
	}
}

// alive reports whether pid is a running process (a zombie is not).
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	return len(f) > 0 && f[0] != "Z" && f[0] != "X"
}

// A tool that does not finish is killed after Timeout, with what it
// started.
func TestRunTimeoutKillsGroup(t *testing.T) {
	r := tool(t, "fake", `sleep 60 &
echo $! > "$1"
echo started
sleep 60
`)
	r.Timeout = 300 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	res, err := r.Run(context.Background(), "", "fake", pidFile)
	if err != nil || !res.TimedOut || res.Exit != -1 || string(res.Out) != "started\n" {
		t.Fatalf("%+v %v", res, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("returned after %v", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for deadline := time.Now().Add(5 * time.Second); alive(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d of the timed-out tool still runs", pid)
		}
	}
}

// Stop kills a validator that is running and removes a scratch copy that
// is there (M3 follow-up 5; through sc's signal handler in
// TestSignalStopsValidator), and once it ran, one that starts after it is
// killed at once: sc is about to end. No test here runs in parallel.
func TestStop(t *testing.T) {
	t.Cleanup(func() { liveMu.Lock(); stopped = false; liveMu.Unlock() })
	r := tool(t, "fake", "echo started\nsleep 60\n")
	home := t.TempDir()
	file, cleanup, err := Scratch(home, "/etc/fstab", []byte("x\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	done := make(chan Result)
	go func() {
		res, err := r.Run(context.Background(), "", "fake")
		if !errors.Is(err, ErrStopped) {
			t.Errorf("killed by Stop: %v", err)
		}
		done <- res
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		liveMu.Lock()
		n := len(liveGroups)
		liveMu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tool never started")
		}
	}
	Stop()
	select {
	case res := <-done:
		if res.Exit != -1 || res.TimedOut {
			t.Errorf("running: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not kill the running tool")
	}
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Errorf("scratch directory still there: %v", err)
	}
	// After Stop nothing starts; a runner told to keep what it starts
	// (sc boot's) still runs it, to the end.
	if res, err := r.Run(context.Background(), "", "fake"); !errors.Is(err, ErrStopped) || len(res.Out) != 0 {
		t.Errorf("started after Stop: %+v %v", res, err)
	}
	keep := tool(t, "fake", "echo done\n")
	keep.KeepOnStop = true
	if res, err := keep.Run(context.Background(), "", "fake"); err != nil || string(res.Out) != "done\n" {
		t.Errorf("KeepOnStop after Stop: %+v %v", res, err)
	}
	// Nothing is made after Stop: it would never be removed (review of
	// chunk F, A8).
	if _, _, err := Scratch(home, "/etc/hosts", []byte("x\n")); !errors.Is(err, ErrStopped) {
		t.Errorf("Scratch after Stop: %v", err)
	}
	if err := scratchMkdir(filepath.Join(home, "tmp", "check-x", "with")); !errors.Is(err, ErrStopped) {
		t.Errorf("scratchMkdir after Stop: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(home, "tmp", "check-*")); len(left) != 0 {
		t.Errorf("made after Stop: %v", left)
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	if len(liveGroups) != 0 || len(liveScratch) != 1 {
		t.Errorf("left: groups %v, scratch %v (the cleanup not run yet holds one)", liveGroups, liveScratch)
	}
}

// A tool that exits while something it started still holds its output does
// not hang the caller: the straggler is killed and the exit status kept.
func TestRunStraggler(t *testing.T) {
	r := tool(t, "fake", `sleep 60 &
echo $! > "$1"
exit $2
`)
	for _, code := range []int{0, 4} {
		pidFile := filepath.Join(t.TempDir(), "pid")
		start := time.Now()
		res, err := r.Run(context.Background(), "", "fake", pidFile, strconv.Itoa(code))
		if err != nil || res.TimedOut || res.Exit != code {
			t.Fatalf("exit %d: %+v %v", code, res, err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("exit %d: returned after %v", code, d)
		}
		b, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		for deadline := time.Now().Add(5 * time.Second); alive(pid); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("exit %d: straggler %d still runs", code, pid)
			}
		}
	}
}

// A cancelled caller is an error, not a result.
func TestRunCancelled(t *testing.T) {
	r := tool(t, "fake", "sleep 60\n")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := r.Run(ctx, "", "fake"); err == nil {
		t.Fatal("no error")
	}
}

// Output past MaxOut is dropped without blocking the tool.
func TestRunOutputCap(t *testing.T) {
	r := tool(t, "fake", "head -c 300000 /dev/zero | tr '\\0' x\necho end >&2\n")
	r.MaxOut = 1000
	res, err := r.Run(context.Background(), "", "fake")
	if err != nil || res.Exit != 0 || !res.Truncated || len(res.Out) != 1000 || string(res.Err) != "end\n" {
		t.Fatalf("exit %d truncated %v len %d err %v", res.Exit, res.Truncated, len(res.Out), err)
	}
}

// The scratch copy is private, keeps the file's name, and is removed.
func TestScratch(t *testing.T) {
	home := filepath.Join(t.TempDir(), "schome")
	file, cleanup, err := Scratch(home, "/etc/systemd/system/ssh.service", []byte("[Unit]\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(file)
	if filepath.Base(file) != "ssh.service" || filepath.Dir(dir) != filepath.Join(home, "tmp") {
		t.Fatalf("file %s", file)
	}
	for p, want := range map[string]os.FileMode{file: 0o600, dir: 0o700, filepath.Join(home, "tmp"): 0o700} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", p, fi.Mode().Perm(), err, want)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != "[Unit]\n" {
		t.Fatalf("content %q", b)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	if _, _, err := Scratch(home, "/", nil); err == nil {
		t.Fatal("/ accepted")
	}
}

// A relative home still gives the validator an absolute path to its copy
// (SC_HOME=home once gave a false clean: the validator ran in the scratch
// directory and could not open its input). Leftover check-* directories
// older than an hour are swept; newer ones and sc edit's kept-* stay.
func TestScratchAbsoluteAndSwept(t *testing.T) {
	t.Chdir(t.TempDir())
	file, cleanup, err := Scratch("home", "/etc/fstab", []byte("x\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !filepath.IsAbs(file) {
		t.Fatalf("relative scratch path %q", file)
	}
	tmp := filepath.Dir(filepath.Dir(file))
	old, fresh, kept := filepath.Join(tmp, "check-old"), filepath.Join(tmp, "check-fresh"), filepath.Join(tmp, "kept-old")
	for _, d := range []string{old, fresh, kept} {
		os.MkdirAll(filepath.Join(d, "root"), 0o700)
	}
	long := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, long, long)
	os.Chtimes(kept, long, long)
	_, cleanup2, err := Scratch("home", "/etc/hosts", nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup2()
	for d, want := range map[string]bool{old: false, fresh: true, kept: true} {
		if _, err := os.Stat(d); (err == nil) != want {
			t.Errorf("%s: exists %v, want %v", filepath.Base(d), err == nil, want)
		}
	}
}

// A validator cut short makes the report incomplete; one that ran,
// whatever it said, does not, nor one that is not installed (sc's own
// rules are then the whole check, before and after alike).
func TestReportIncomplete(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   bool
	}{
		{"", false}, // not installed
		{"#!/bin/sh\nexit 0\n", false},
		{"#!/bin/sh\necho '   [E] something' \nexit 1\n", false},
		{"#!/bin/sh\nkill -9 $$\n", true},
		{"#!/bin/sh\nsleep 60\n", true},
		{"#!/bin/sh\nhead -c 5000 /dev/zero\nexit 0\n", true},
	} {
		c, _ := fakeMachine(t, []string{"/dev/sda3"}, "", "", 0)
		c.Run.Timeout, c.Run.MaxOut = 300*time.Millisecond, 1000
		if tc.script != "" {
			os.WriteFile(filepath.Join(c.Run.Dirs[0], "findmnt"), []byte(tc.script), 0o755)
		}
		rep, err := c.Check(context.Background(), "/etc/fstab", []byte("/dev/sda3 /data ext4 defaults 0 2\n"))
		if err != nil || rep.Incomplete != tc.want {
			t.Errorf("%q: incomplete %v, want %v (%v)", tc.script, rep.Incomplete, tc.want, err)
		}
	}
}

// A time that runs out before the tool starts is a timeout, not an error:
// the race run, on a busy machine, once took more than 200 ms to get to
// the start, and exec refuses to start under a context that is done.
func TestRunTimeoutBeforeStart(t *testing.T) {
	r := tool(t, "fake", "echo started\n")
	r.Timeout = time.Nanosecond
	res, err := r.Run(context.Background(), "", "fake")
	if err != nil || !res.TimedOut || res.Exit != -1 || len(res.Out) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A caller's context that is done before the start is the caller's error,
// not a timeout of the tool's.
func TestRunCancelledBeforeStart(t *testing.T) {
	r := tool(t, "fake", "echo started\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := r.Run(ctx, "", "fake")
	if !errors.Is(err, context.Canceled) || res.TimedOut {
		t.Fatalf("%+v %v", res, err)
	}
}

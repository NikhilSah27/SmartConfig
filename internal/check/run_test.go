//go:build linux

package check

import (
	"bytes"
	"context"
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
	for _, want := range []string{"arg:-f\narg:a b\narg:$(id)\narg:\n", "LC_ALL=C\n", "PATH=/usr/sbin:/usr/bin:/sbin:/bin\n", dir + "\n", "to stderr\n"} {
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
	for _, name := range []string{"", "../fake", "/bin/true", "a/b"} {
		if _, err := r.Run(context.Background(), "", name); err == nil {
			t.Errorf("%q accepted", name)
		}
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
	if err != nil || res.Exit != 0 || !res.Truncated || len(res.Out) != 1000 {
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

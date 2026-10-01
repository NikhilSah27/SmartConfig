package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"smartconfig/internal/store"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startWatch runs "sc watch args..." through runContext in a goroutine and
// returns a stop function that cancels it and returns its exit code.
func startWatch(t *testing.T, args ...string) (stderr *lockedBuf, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stderr = &lockedBuf{}
	done := make(chan int, 1)
	go func() {
		done <- runContext(ctx, append([]string{"watch"}, args...), &lockedBuf{}, stderr)
	}()
	var once sync.Once
	code := -1
	stop = func() int {
		once.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("sc watch did not stop")
			}
		})
		return code
	}
	t.Cleanup(func() { stop() })
	return stderr, stop
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// End to end: sc watch --root DIR records a new file, and stops with exit
// 0 when its context is cancelled.
func TestWatchEndToEnd(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home") // created by sc watch
	t.Setenv("SC_HOME", home)
	dir := t.TempDir()
	stderr, stop := startWatch(t, "--root", dir)
	waitUntil(t, "the baseline", func() bool { return strings.Contains(stderr.String(), "baseline: ") })
	p := filepath.Join(dir, "conf")
	os.WriteFile(p, []byte("x\n"), 0o644)
	waitUntil(t, "the row", func() bool {
		s, err := store.Open(home)
		if err != nil {
			return false
		}
		defer s.Close()
		cs, _ := s.List(p, 0)
		return len(cs) == 2 // did not exist, created
	})
	if code := stop(); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr.String())
	}
	// Lines carry the time of day, not journald's "<N>" (no JOURNAL_STREAM).
	for _, l := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if strings.HasPrefix(l, "<") || len(l) < 9 || l[2] != ':' || l[5] != ':' {
			t.Errorf("line %q", l)
		}
	}
	if !strings.Contains(stderr.String(), " T4 "+p+": created (") {
		t.Errorf("no line for the new file:\n%s", stderr.String())
	}
}

// Each refusal is one line, exit 1, and creates nothing.
func TestWatchRefusals(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "sub", "home")
	t.Setenv("SC_HOME", inside)
	r := sc(t, "watch", "--root", dir)
	if r.code != 1 || r.stderr != "sc: SC_HOME "+inside+" is inside watched root "+dir+"\n" || r.stdout != "" {
		t.Errorf("SC_HOME inside a root: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Error("something was created")
	}

	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	stderr, _ := startWatch(t, "--root", dir)
	waitUntil(t, "the first watcher", func() bool { return strings.Contains(stderr.String(), "baseline: ") })
	r = sc(t, "watch", "--root", dir)
	want := fmt.Sprintf("sc: scd already running on %s (pid %d)\n", home, os.Getpid())
	if r.code != 1 || r.stderr != want {
		t.Errorf("second instance: %+v, want %q", r, want)
	}

	if os.Geteuid() != 0 {
		r = sc(t, "watch")
		if r.code != 1 || !strings.HasPrefix(r.stderr, "sc: sc watch needs root") || strings.Count(r.stderr, "\n") != 1 {
			t.Errorf("not root: %+v", r)
		}
	}
}

// The "<N>" prefix is kept only when JOURNAL_STREAM names stderr.
func TestLogWriterPrefix(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var st syscall.Stat_t
	syscall.Fstat(int(f.Fd()), &st)
	match := fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	for _, c := range []struct {
		env, want string
	}{
		{match, "<4>T1 /etc/fstab: changed (1a2b3c)\n<6>baseline: 1 first seen\n"},
		{fmt.Sprintf("%d:%d", st.Dev, st.Ino+1), "15:04:05 T1 /etc/fstab: changed (1a2b3c)\n15:04:05 baseline: 1 first seen\n"},
		{"", "15:04:05 T1 /etc/fstab: changed (1a2b3c)\n15:04:05 baseline: 1 first seen\n"},
		{"garbage", "15:04:05 T1 /etc/fstab: changed (1a2b3c)\n15:04:05 baseline: 1 first seen\n"},
	} {
		var out bytes.Buffer
		w := &logWriter{out: &out, journal: isJournal(f, c.env), now: func() time.Time {
			return time.Date(2026, 10, 1, 15, 4, 5, 0, time.Local)
		}}
		w.Write([]byte("<4>T1 /etc/fstab: changed (1a2b3c)\n"))
		w.Write([]byte("<6>baseline: 1 first seen\n"))
		if out.String() != c.want {
			t.Errorf("JOURNAL_STREAM=%q: %q, want %q", c.env, out.String(), c.want)
		}
	}
}

// The real binary stops with exit 0 on SIGTERM, as systemd expects.
func TestWatchSIGTERM(t *testing.T) {
	bin := scBinary(t)
	dir := t.TempDir()
	cmd := exec.Command(bin, "watch", "--root", dir)
	cmd.Env = append(os.Environ(), "SC_HOME="+filepath.Join(t.TempDir(), "home"))
	errp, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(errp)
	for lines.Scan() && !strings.Contains(lines.Text(), "baseline: ") {
	}
	go func() {
		for lines.Scan() {
		}
	}()
	cmd.Process.Signal(syscall.SIGTERM)
	err := cmd.Wait()
	if err != nil {
		t.Fatalf("after SIGTERM: %v", err)
	}
}

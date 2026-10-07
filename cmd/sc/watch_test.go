package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
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
	waitUntilWithin(t, 10*time.Second, what, cond)
}

// waitUntilWithin is waitUntil with its own upper bound.
func waitUntilWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
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
	t.Setenv("JOURNAL_STREAM", "")             // the check below expects terminal lines
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

// watchProc starts the real binary watching a temp dir and returns it
// once its baseline is logged, with its stderr so far.
func watchProc(t *testing.T, env []string) (*exec.Cmd, *lockedBuf) {
	t.Helper()
	cmd := exec.Command(scBinary(t), "watch", "--root", t.TempDir())
	cmd.Env = append(append(os.Environ(), "SC_HOME="+filepath.Join(t.TempDir(), "home"), "JOURNAL_STREAM="), env...)
	errb := &lockedBuf{}
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	waitUntil(t, "the baseline", func() bool { return strings.Contains(errb.String(), "baseline: ") })
	return cmd, errb
}

// The real binary stops with exit 0 on SIGTERM and SIGINT, as systemd
// expects, and says so.
func TestWatchStopSignals(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		cmd, errb := watchProc(t, nil)
		cmd.Process.Signal(sig)
		if err := cmd.Wait(); err != nil {
			t.Errorf("after %v: %v; stderr:\n%s", sig, err, errb.String())
		}
		if !strings.HasSuffix(errb.String(), " stopped\n") {
			t.Errorf("after %v, no stop line:\n%s", sig, errb.String())
		}
	}
}

// A broken stderr pipe (journald gone) must not kill sc watch with
// SIGPIPE, which systemd would not restart (chunk E review).
func TestWatchBrokenStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	cmd := exec.Command(scBinary(t), "watch", "--root", t.TempDir())
	cmd.Env = append(os.Environ(), "SC_HOME="+filepath.Join(t.TempDir(), "home"))
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	time.Sleep(time.Second) // the baseline lines hit the broken pipe
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("sc watch died: %v", cmd.Wait())
	}
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("after SIGTERM: %v", err)
	}
}

// A stop signal while New waits for the store still ends with exit 0.
func TestWatchSignalDuringStartup(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	os.Mkdir(home, 0o700)
	// A new, empty database must be migrated under the write lock, so New
	// waits while another connection holds it.
	db := filepath.Join(home, "changes.db")
	os.WriteFile(db, nil, 0o600)
	release := holdWriteLock(t, db)
	cmd := exec.Command(scBinary(t), "watch", "--root", t.TempDir())
	cmd.Env = append(os.Environ(), "SC_HOME="+home)
	errb := &lockedBuf{}
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
	release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("signal during startup: %v; stderr:\n%s", err, errb.String())
	}
	if strings.Contains(errb.String(), "interrupted") {
		t.Fatalf("stderr:\n%s", errb.String())
	}
}

// A fatal error is logged at err priority under journald only.
func TestWatchErrorPriority(t *testing.T) {
	dir := t.TempDir()
	for _, journal := range []bool{true, false} {
		f, err := os.CreateTemp(t.TempDir(), "stderr")
		if err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		syscall.Fstat(int(f.Fd()), &st)
		cmd := exec.Command(scBinary(t), "watch", "--root", dir)
		cmd.Env = append(os.Environ(), "SC_HOME="+filepath.Join(dir, "home"))
		if journal {
			cmd.Env = append(cmd.Env, fmt.Sprintf("JOURNAL_STREAM=%d:%d", st.Dev, st.Ino))
		} else {
			cmd.Env = append(cmd.Env, "JOURNAL_STREAM=")
		}
		cmd.Stderr = f
		cmd.Run()
		f.Close()
		b, _ := os.ReadFile(f.Name())
		want := "sc: SC_HOME "
		if journal {
			want = "<3>" + want
		}
		if !strings.HasPrefix(string(b), want) {
			t.Errorf("journal %v: %q", journal, b)
		}
	}
}

// --root takes only existing real directories other than /.
func TestWatchBadRoots(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	os.WriteFile(file, nil, 0o644)
	link := filepath.Join(dir, "l")
	os.Symlink(dir, link)
	for _, root := range []string{"", "/", filepath.Join(dir, "missing"), file, link} {
		r := sc(t, "watch", "--root", root)
		if r.code != 1 || !strings.HasPrefix(r.stderr, "sc: --root") || strings.Count(r.stderr, "\n") != 1 {
			t.Errorf("--root %q: %+v", root, r)
		}
	}
	if abs, err := checkRoots([]string{dir, dir + "/", dir}); err != nil || len(abs) != 1 {
		t.Errorf("duplicates: %v %v", abs, err)
	}
}

// newLogWriter checks the file it is given against JOURNAL_STREAM and
// writes to out.
func TestNewLogWriter(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "stderr")
	defer f.Close()
	var st syscall.Stat_t
	syscall.Fstat(int(f.Fd()), &st)
	t.Setenv("JOURNAL_STREAM", fmt.Sprintf("%d:%d", st.Dev, st.Ino))
	var out bytes.Buffer
	newLogWriter(f, &out).Write([]byte("<4>x\n"))
	if out.String() != "<4>x\n" {
		t.Fatalf("%q", out.String())
	}
}

// SIGHUP asks for a rescan and keeps watching: a change no event reported
// (a write through a hard link from an unwatched directory) is found, and
// SIGTERM still stops with exit 0.
func TestWatchSIGHUPRescans(t *testing.T) {
	root, home := t.TempDir(), filepath.Join(t.TempDir(), "home")
	conf := filepath.Join(root, "conf")
	os.WriteFile(conf, []byte("one\n"), 0o644)
	cmd := exec.Command(scBinary(t), "watch", "--root", root)
	cmd.Env = append(os.Environ(), "SC_HOME="+home, "JOURNAL_STREAM=")
	errb := &lockedBuf{}
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	waitUntil(t, "the baseline", func() bool { return strings.Contains(errb.String(), "baseline: ") })
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Link(conf, alias); err != nil {
		t.Skip(err)
	}
	f, _ := os.OpenFile(alias, os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString("two\n")
	f.Close()
	time.Sleep(200 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGHUP)
	// sc watch keeps 10 s (RescanMinGap) between rescans, counted from the
	// startup one: the row comes about 10 s after the start, more on a
	// slow machine.
	waitUntilWithin(t, 60*time.Second, "the rescan row", func() bool { return strings.Contains(errb.String(), conf+": changed (found by rescan)") })
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("after SIGHUP then SIGTERM: %v\n%s", err, errb.String())
	}
}

// sdNotify sends its state as one datagram to $NOTIFY_SOCKET, and does
// nothing without it (sc watch not under systemd).
func TestSdNotify(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := sdNotify("READY=1"); err != nil {
		t.Errorf("without a socket: %v", err)
	}
	sock := filepath.Join(t.TempDir(), "notify")
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	t.Setenv("NOTIFY_SOCKET", sock)
	if err := sdNotify("READY=1"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	l.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := l.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Errorf("got %q %v", buf[:n], err)
	}
}

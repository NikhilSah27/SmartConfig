package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	scBin     string
	buildErr  error
)

// scBinary builds sc once for all signal tests.
func scBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sc-signal-test-")
		if err != nil {
			buildErr = err
			return
		}
		scBin = filepath.Join(dir, "sc")
		if out, err := exec.Command("go", "build", "-tags", "sctest", "-o", scBin, ".").CombinedOutput(); err != nil {
			buildErr = errors.New(string(out))
		}
	})
	if buildErr != nil {
		t.Fatalf("build: %v", buildErr)
	}
	return scBin
}

type restoreSetup struct {
	bin, id, path, db string
	env               []string
}

// newRestoreSetup makes a store with one snapshot of a file ("good"), then
// changes the file ("bad"), ready for "sc restore id".
func newRestoreSetup(t *testing.T) restoreSetup {
	t.Helper()
	bin := scBinary(t)
	home := t.TempDir()
	env := append(os.Environ(), "SC_HOME="+home)
	run := func(args ...string) string {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sc %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	p := filepath.Join(t.TempDir(), "conf")
	os.WriteFile(p, []byte("good\n"), 0o644)
	run("init")
	id := run("snapshot", "-q", p)
	os.WriteFile(p, []byte("bad\n"), 0o644)
	return restoreSetup{bin: bin, id: id, path: p, db: filepath.Join(home, "changes.db"), env: env}
}

// holdWriteLock takes the database write lock until the returned function
// is called (or the test ends).
func holdWriteLock(t *testing.T, dbPath string) (release func()) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			db.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// startRestore starts sc restore (optionally through a wrapper command) and
// waits until it has prepared its temp file.
func startRestore(t *testing.T, r restoreSetup, prefix ...string) (*exec.Cmd, *lockedBuffer, *lockedBuffer) {
	t.Helper()
	args := append(append([]string{}, prefix...), r.bin, "restore", r.id)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = r.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr lockedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(r.path), ".conf.sc-tmp-*")); len(tmps) == 1 {
			return cmd, &stdout, &stderr
		}
		if time.Now().After(deadline) {
			t.Fatal("restore never prepared its temp file")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitEnd waits for cmd and checks how it ended: by signal want, or with
// exit status 1 if want is 0.
func waitEnd(t *testing.T, cmd *exec.Cmd, stderr fmt.Stringer, want syscall.Signal) {
	t.Helper()
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("ended with %v, stderr %q", err, stderr.String())
	}
	ws := exit.Sys().(syscall.WaitStatus)
	switch {
	case want != 0 && !(ws.Signaled() && ws.Signal() == want):
		t.Fatalf("want death by %v, got %v, stderr %q", want, ws, stderr.String())
	case want == 0 && ws.ExitStatus() != 1:
		t.Fatalf("want exit status 1, got %v, stderr %q", ws, stderr.String())
	}
	if msg := stderr.String(); strings.Count(msg, "\n") != 1 || strings.Contains(msg, "goroutine") {
		t.Fatalf("stderr must be one line, no stack trace: %q", msg)
	}
}

func checkUnchanged(t *testing.T, r restoreSetup) {
	t.Helper()
	if tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(r.path), ".conf.sc-tmp-*")); len(tmps) != 0 {
		t.Fatalf("temp file left: %v", tmps)
	}
	if b, _ := os.ReadFile(r.path); string(b) != "bad\n" {
		t.Fatalf("target changed: %q", b)
	}
}

// One Ctrl-C while a restore waits for the database: it stops at the next
// safe point (within one busy timeout), says the file was not changed,
// leaves no temp file, and ends by SIGINT.
func TestInterruptedRestoreRemovesTempFile(t *testing.T) {
	r := newRestoreSetup(t)
	holdWriteLock(t, r.db)
	cmd, _, stderr := startRestore(t, r)
	cmd.Process.Signal(syscall.SIGINT)
	waitEnd(t, cmd, stderr, syscall.SIGINT)
	if !strings.Contains(stderr.String(), "interrupted (file not changed)") {
		t.Fatalf("stderr %q", stderr.String())
	}
	checkUnchanged(t, r)
}

// A second Ctrl-C before the rename stops at once.
func TestSecondSignalBeforeRenameStopsAtOnce(t *testing.T) {
	r := newRestoreSetup(t)
	holdWriteLock(t, r.db)
	cmd, _, stderr := startRestore(t, r)
	cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cmd.Process.Signal(syscall.SIGINT)
	waitEnd(t, cmd, stderr, syscall.SIGINT)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("second signal took %v", d)
	}
	if !strings.Contains(stderr.String(), "(file not changed)") {
		t.Fatalf("stderr %q", stderr.String())
	}
	checkUnchanged(t, r)
}

// A second Ctrl-C that arrives after the command has printed its line, while
// sc is about to end by the first one, prints nothing more.
func TestSecondSignalAfterReportPrintsNothing(t *testing.T) {
	r := newRestoreSetup(t)
	r.env = append(r.env, "SC_TEST_AFTER_RUN=3s")
	release := holdWriteLock(t, r.db)
	cmd, _, stderr := startRestore(t, r)
	cmd.Process.Signal(syscall.SIGINT)
	release() // the restore gets the lock and stops at its next safe point
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stderr.String(), "(file not changed)") {
		if time.Now().After(deadline) {
			t.Fatalf("the restore never reported; stderr %q", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cmd.Process.Signal(syscall.SIGINT)
	err := cmd.Wait()
	ws, ok := exitStatus(err)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("ended with %v, stderr %q", err, stderr.String())
	}
	if msg := stderr.String(); strings.Count(msg, "\n") != 1 {
		t.Fatalf("stderr must be one line: %q", msg)
	}
	checkUnchanged(t, r)
}

// lockedBuffer is a strings.Builder that a test may read while the command
// is still writing to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func exitStatus(err error) (syscall.WaitStatus, bool) {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, false
	}
	return exit.Sys().(syscall.WaitStatus), true
}

// SIGQUIT and SIGABRT would normally print a Go stack trace: sc prints one
// line and exits 1 instead.
func TestQuitAndAbortPrintNoStackTrace(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGQUIT, syscall.SIGABRT} {
		t.Run(sig.String(), func(t *testing.T) {
			r := newRestoreSetup(t)
			holdWriteLock(t, r.db)
			cmd, _, stderr := startRestore(t, r)
			cmd.Process.Signal(sig)
			waitEnd(t, cmd, stderr, 0)
			checkUnchanged(t, r)
		})
	}
}

// A SIGINT the caller chose to ignore stays ignored: the restore finishes.
func TestIgnoredSIGINTStaysIgnored(t *testing.T) {
	r := newRestoreSetup(t)
	release := holdWriteLock(t, r.db)
	cmd, _, stderr := startRestore(t, r, "sh", "-c", `trap "" INT; exec "$@"`, "sh")
	cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(300 * time.Millisecond)
	release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("restore stopped by an ignored SIGINT: %v %q", err, stderr.String())
	}
	if b, _ := os.ReadFile(r.path); string(b) != "good\n" {
		t.Fatalf("not restored: %q", b)
	}
}

// Ctrl-C in a terminal goes to the whole foreground group: a shell loop
// around sc must stop, not run its next step.
func TestCtrlCStopsShellLoop(t *testing.T) {
	r := newRestoreSetup(t)
	holdWriteLock(t, r.db)
	cmd, stdout, _ := startRestore(t, r, "bash", "-c", `for i in 1 2; do "$@"; echo NEXT; done`, "bash")
	syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the shell loop kept running")
	}
	if strings.Contains(stdout.String(), "NEXT") {
		t.Fatalf("the loop ran its next step after Ctrl-C: %q", stdout.String())
	}
}

// Two Ctrl-C after the restore renamed its file, while its COMMIT is held up
// by a reader: sc must still report that the file was replaced and name the
// saved previous content, not just "interrupted".
func TestSecondSignalAfterRenameStillReports(t *testing.T) {
	for attempt := 1; attempt <= 5; attempt++ {
		if secondSignalAfterRename(t) {
			return
		}
		t.Logf("attempt %d: did not catch the restore between rename and commit, retrying", attempt)
	}
	t.Fatal("could not hold a restore between its rename and its commit in 5 attempts")
}

func secondSignalAfterRename(t *testing.T) (caught bool) {
	r := newRestoreSetup(t)
	db, err := sql.Open("sqlite", "file:"+r.db+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	cmd := exec.Command(r.bin, "restore", r.id)
	// The pause gives the loop below time to take its read lock between the
	// pre-restore commit and the restore's own transaction, however fast
	// the disk is.
	cmd.Env = append(r.env, "SC_TEST_BEFORE_RESTORE_LOCK=1s")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	// Take a read lock as soon as the pre-restore row is committed, so the
	// restore's own COMMIT, which comes after its rename, cannot finish.
	deadline := time.Now().Add(5 * time.Second)
	holding := false
	for !holding && time.Now().Before(deadline) {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			continue
		}
		var pre int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM changes WHERE origin = 'pre-restore'`).Scan(&pre); err == nil && pre == 1 {
			holding = true
			break
		}
		conn.ExecContext(ctx, "ROLLBACK")
	}
	if !holding {
		return false
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(r.path); string(b) == "good\n" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var restoreRows int
	conn.QueryRowContext(ctx, `SELECT count(*) FROM changes WHERE origin = 'restore'`).Scan(&restoreRows)
	if b, _ := os.ReadFile(r.path); string(b) != "good\n" || restoreRows != 0 {
		return false // the restore finished before the read lock was taken
	}
	cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(150 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGINT)
	waitEnd(t, cmd, &stderr, syscall.SIGINT)
	if msg := stderr.String(); !strings.Contains(msg, "file restored but not recorded") || !strings.Contains(msg, "previous content saved as") {
		t.Fatalf("after a rename, stderr must say so and name the saved id: %q", msg)
	}
	return true
}

// Ctrl-C and Ctrl-\ while sc edit's editor runs go to the whole foreground
// group and are the editor's to handle: sc waits for the editor and then
// saves, as sudoedit and git do. It used to die at once, leaving the
// editor behind and its work unsaved (chunk B review).
func TestEditLeavesCtrlCToTheEditor(t *testing.T) {
	bin := scBinary(t)
	home := filepath.Join(t.TempDir(), "home")
	dir := t.TempDir()
	file := filepath.Join(dir, "conf")
	os.WriteFile(file, []byte("before\n"), 0o644)
	script := filepath.Join(dir, "editor")
	os.WriteFile(script, []byte("#!/bin/sh\nkill -INT $PPID\nkill -QUIT $PPID\nsleep 0.5\necho edited > \"$1\"\n"), 0o755)
	env := append(os.Environ(), "SC_HOME="+home, "SUDO_EDITOR=", "VISUAL=", "EDITOR="+script)
	initCmd := exec.Command(bin, "init")
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	cmd := exec.Command(bin, "edit", file)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(file)
	if err != nil || string(b) != "edited\n" || !strings.Contains(string(out), "saved "+file+" as ") {
		t.Fatalf("err %v, file %q, output %q", err, b, out)
	}
}

// A hangup of the rescue console (its getty starting) does not end
// sc status --console: the report is still written. Plain sc status
// still ends by SIGHUP.
func TestConsoleStatusOutlivesHangup(t *testing.T) {
	bin := scBinary(t)
	home := t.TempDir()
	env := append(os.Environ(), "SC_HOME="+home)
	init := exec.Command(bin, "init")
	init.Env = env
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("sc init: %v %s", err, out)
	}
	for _, tc := range []struct {
		args []string
		want syscall.Signal // 0: exit status 0
	}{
		{[]string{"status", "--console"}, 0},
		{[]string{"status"}, syscall.SIGHUP},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			// The hangup comes while the command runs and its report is
			// still to be written: that is the getty's race. (After the
			// report it proves nothing: the chunk D review moved the fix
			// behind the write and the test still passed.)
			cmd.Env = append(env, "SC_TEST_IN_STATUS=2s")
			var stdout, stderr lockedBuffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill() })
			deadline := time.Now().Add(30 * time.Second)
			for !strings.Contains(stderr.String(), "sctest: in status") {
				if time.Now().After(deadline) {
					t.Fatalf("sc status never began; stdout %q stderr %q", stdout.String(), stderr.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if s := stdout.String(); s != "" {
				t.Fatalf("output before the hangup, so it proves nothing: %q", s)
			}
			cmd.Process.Signal(syscall.SIGHUP)
			err := cmd.Wait()
			if tc.want == 0 {
				if err != nil || !strings.Contains(stdout.String(), "This boot:") {
					t.Fatalf("ended with %v, stdout %q stderr %q", err, stdout.String(), stderr.String())
				}
				return
			}
			if ws, ok := exitStatus(err); !ok || !ws.Signaled() || ws.Signal() != tc.want {
				t.Fatalf("ended with %v, want death by %v", err, tc.want)
			}
		})
	}
}

// TestConsoleStatusGivesWayToTheShell: systemd ends rescue.service when its
// ExecStartPre takes over 90 s, and then there is no shell. A report that
// hangs stops itself long before, says so on the console and exits 1,
// which the drop-in's "-" forgives.
func TestConsoleStatusGivesWayToTheShell(t *testing.T) {
	bin := scBinary(t)
	home := t.TempDir()
	env := append(os.Environ(), "SC_HOME="+home)
	init := exec.Command(bin, "init")
	init.Env = env
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("sc init: %v %s", err, out)
	}
	run := func(args []string, extra ...string) (string, int, time.Duration) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(env, extra...)
		start := time.Now()
		out, err := cmd.Output()
		code := 0
		if ws, ok := exitStatus(err); ok {
			code = ws.ExitStatus()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code, time.Since(start)
	}
	out, code, took := run([]string{"status", "--console"}, "SC_TEST_IN_STATUS=20s", "SC_TEST_CONSOLE_LIMIT=300ms")
	if code != 1 || took > 10*time.Second || strings.Contains(out, "This boot:") ||
		!strings.Contains(out, "sc: the report took over 300ms and was stopped") || !strings.Contains(out, "sc status\n") {
		t.Errorf("a hung report: exit %d after %s, stdout %q", code, took, out)
	}
	// In time: the report, once, and no word of a stop.
	out, code, _ = run([]string{"status", "--console"}, "SC_TEST_IN_STATUS=100ms", "SC_TEST_CONSOLE_LIMIT=30s")
	if code != 0 || strings.Count(out, "This boot:") != 1 || strings.Contains(out, "stopped") {
		t.Errorf("a report in time: exit %d, stdout %q", code, out)
	}
	// Only --console has the limit: a slow sc status is the owner's to wait for.
	out, code, _ = run([]string{"status"}, "SC_TEST_IN_STATUS=600ms", "SC_TEST_CONSOLE_LIMIT=100ms")
	if code != 0 || !strings.Contains(out, "This boot:") {
		t.Errorf("sc status: exit %d, stdout %q", code, out)
	}
}

func TestConsoleStatusArgs(t *testing.T) {
	for _, tc := range []struct {
		args string
		want bool
	}{
		{"status --console", true}, {"status --console=true", true}, {"status", false}, {"status --console=false", false},
		{"watch --console", false}, {"", false}, {"log status --console", false},
	} {
		if got := consoleStatus(strings.Fields(tc.args)); got != tc.want {
			t.Errorf("consoleStatus(%q) = %v", tc.args, got)
		}
	}
}

// A signal to sc check kills the validator it waits for, with what that
// started, and removes the scratch copy at once (M3 follow-up 5). The
// validator has a process group of its own, which a Ctrl-C at the
// terminal does not reach, and sc ends without its deferred cleanups.
func TestSignalStopsValidator(t *testing.T) {
	bin := scBinary(t)
	home, tools := t.TempDir(), t.TempDir()
	pids := filepath.Join(tools, "pids")
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nsleep 60 &\necho $$ $! > "+pids+".tmp\nmv "+pids+".tmp "+pids+"\nwait\n"), 0o755)
	cand := filepath.Join(t.TempDir(), "fstab")
	os.WriteFile(cand, []byte("/dev/null /data ext4 defaults 0 2\n"), 0o644)
	gone := func(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		os.Remove(pids)
		cmd := exec.Command(bin, "check", "--as", "/etc/fstab", cand)
		cmd.Env = append(os.Environ(), "SC_HOME="+home, "SC_TEST_TOOLS="+tools)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var stdout, stderr lockedBuffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill() })
		var started []int
		for deadline := time.Now().Add(5 * time.Second); len(started) == 0; time.Sleep(10 * time.Millisecond) {
			if b, err := os.ReadFile(pids); err == nil {
				for _, f := range strings.Fields(string(b)) {
					var pid int
					fmt.Sscan(f, &pid)
					started = append(started, pid)
				}
			} else if time.Now().After(deadline) {
				t.Fatal("the validator never started")
			}
		}
		t.Cleanup(func() {
			for _, pid := range started {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		})
		begin := time.Now()
		cmd.Process.Signal(sig)
		waitEnd(t, cmd, &stderr, sig)
		if d := time.Since(begin); d > 3*time.Second {
			t.Errorf("%v: sc took %v to end", sig, d)
		}
		for _, pid := range started {
			for deadline := time.Now().Add(5 * time.Second); !gone(pid) && time.Now().Before(deadline); {
				time.Sleep(20 * time.Millisecond)
			}
			if !gone(pid) {
				t.Errorf("%v: validator process %d still runs", sig, pid)
			}
		}
		if left, _ := filepath.Glob(filepath.Join(home, "tmp", "check-*")); len(left) != 0 {
			t.Errorf("%v: scratch copies left: %v", sig, left)
		}
	}
}

// A signal while sc check has more files to check: Stop kills the first
// file's validator, and the command must not go on to make the next file's
// scratch copy before sc ends (review of chunk F, B1: 8 of 20 runs left
// one).
func TestSignalScratchNextTarget(t *testing.T) {
	bin := scBinary(t)
	leaks := 0
	const runs = 20
	for i := 0; i < runs; i++ {
		home, tools := t.TempDir(), t.TempDir()
		pids := filepath.Join(tools, "pids")
		os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\nif mkdir "+tools+"/first 2>/dev/null; then sleep 60 & echo $$ $! > "+pids+".tmp; mv "+pids+".tmp "+pids+"; wait; fi\nexit 0\n"), 0o755)
		args := []string{"check"}
		for j := 0; j < 40; j++ {
			args = append(args, "/etc/fstab")
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "SC_HOME="+home, "SC_TEST_TOOLS="+tools)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(pids); err == nil {
				break
			} else if time.Now().After(deadline) {
				t.Fatal("the validator never started")
			}
		}
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		if left, _ := filepath.Glob(filepath.Join(home, "tmp", "check-*")); len(left) != 0 {
			leaks++
			files, _ := filepath.Glob(filepath.Join(left[0], "*"))
			t.Logf("run %d: scratch left: %v %v", i, left, files)
		}
		b, _ := os.ReadFile(pids)
		exec.Command("sh", "-c", "kill -9 "+string(b)).Run()
	}
	if leaks != 0 {
		t.Errorf("%d of %d runs left a scratch copy behind", leaks, runs)
	}
}

// A signal while sc check starts validators one after another: one that
// was being started when Stop ran must not run on after sc died (B2: 10
// of 30 runs left one). The fake validator hangs once the marker exists,
// which the test makes just before the signal.
func TestSignalValidatorStarting(t *testing.T) {
	bin := scBinary(t)
	args := []string{"check"}
	for j := 0; j < 300; j++ {
		args = append(args, "/etc/fstab")
	}
	survived := 0
	const runs = 30
	for i := 0; i < runs; i++ {
		home, tools := t.TempDir(), t.TempDir()
		marker := filepath.Join(tools, "marker")
		tag := fmt.Sprintf("31.%d%d", os.Getpid()%1000, i)
		os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\n[ -e "+marker+" ] && exec sleep "+tag+"\nexit 0\n"), 0o755)
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "SC_HOME="+home, "SC_TEST_TOOLS="+tools)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(100+rand.Intn(300)) * time.Millisecond)
		os.WriteFile(marker, nil, 0o644)
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		time.Sleep(300 * time.Millisecond)
		if out, err := exec.Command("pgrep", "-f", "^sleep "+tag+"$").Output(); err == nil {
			survived++
			t.Logf("run %d: validator still runs after sc died: pid %s", i, strings.TrimSpace(string(out)))
			exec.Command("pkill", "-f", "^sleep "+tag+"$").Run()
		}
	}
	if survived != 0 {
		t.Errorf("%d of %d runs left a validator running", survived, runs)
	}
}

// What sc boot runs (grub-editenv rewrites grubenv in place) finishes when
// a signal ends sc, as before Stop: the fake truncates grubenv as
// grub-editenv's fopen("wb") does and writes the block a moment later; a
// SIGTERM to sc boot seen in between (B4: grubenv was left empty).
func TestSignalKeepsGrubEditenv(t *testing.T) {
	bin := scBinary(t)
	home, tools := t.TempDir(), t.TempDir()
	env := filepath.Join(t.TempDir(), "grubenv")
	block := "# GRUB Environment Block\n" + strings.Repeat("#", 1024-len("# GRUB Environment Block\n"))
	os.WriteFile(env, []byte(block), 0o644)
	started := filepath.Join(tools, "started")
	os.WriteFile(filepath.Join(tools, "grub-editenv"), []byte("#!/bin/sh\n: > \"$1\"\ntouch "+started+"\nsleep 1\nprintf '%s' '"+block+"' > \"$1\"\n"), 0o755)
	cmd := exec.Command(bin, "boot", "seen")
	cmd.Env = append(os.Environ(), "SC_HOME="+home, "SC_TEST_BOOT_TOOLS="+tools, "SC_TEST_GRUBENV="+env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("grub-editenv never started")
		}
	}
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	time.Sleep(2 * time.Second)
	if fi, err := os.Stat(env); err != nil || fi.Size() != 1024 {
		t.Errorf("grubenv after a SIGTERM to sc boot seen: size %d %v, want 1024 (grub-editenv was killed mid-write)", fi.Size(), err)
	}
}

// A slow report on the rescue console shows its header first, so a hung sc
// still says which boot was healthy before the stop (your pick for the
// chunk C review's note); one in time is the report once, whole.
func TestConsoleStatusHeaderFirst(t *testing.T) {
	bin := scBinary(t)
	home := t.TempDir()
	conf := filepath.Join(t.TempDir(), "conf")
	os.WriteFile(conf, []byte("x\n"), 0o644)
	env := append(os.Environ(), "SC_HOME="+home)
	for _, args := range [][]string{{"init"}, {"snapshot", conf}} {
		c := exec.Command(bin, args...)
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("sc %v: %v %s", args, err, out)
		}
	}
	run := func(extra ...string) (string, int) {
		cmd := exec.Command(bin, "status", "--console")
		cmd.Env = append(env, extra...)
		out, err := cmd.Output()
		code := 0
		if ws, ok := exitStatus(err); ok {
			code = ws.ExitStatus()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	out, code := run("SC_TEST_STATUS_CHECKS=20s", "SC_TEST_CONSOLE_HEAD=100ms", "SC_TEST_CONSOLE_LIMIT=1s")
	if code != 1 || !strings.HasPrefix(out, "This boot:") || !strings.Contains(out, "\nscd:") ||
		!strings.Contains(out, "sc: the report took over 1 s and was stopped") || strings.Contains(out, "\nThe newest changes") {
		t.Errorf("a hung report: exit %d, stdout %q", code, out)
	}
	out, code = run("SC_TEST_STATUS_CHECKS=300ms", "SC_TEST_CONSOLE_HEAD=100ms", "SC_TEST_CONSOLE_LIMIT=30s")
	if code != 0 || strings.Count(out, "This boot:") != 1 || !strings.Contains(out, "\nThe newest changes") || strings.Contains(out, "stopped") {
		t.Errorf("a slow report in time: exit %d, stdout %q", code, out)
	}
}

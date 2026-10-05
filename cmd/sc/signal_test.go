package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
			// The window after the command has returned: --console has
			// set up its signals by then.
			cmd.Env = append(env, "SC_TEST_AFTER_RUN=3s")
			var stdout, stderr lockedBuffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill() })
			deadline := time.Now().Add(10 * time.Second)
			for !strings.Contains(stdout.String(), "This boot:") {
				if time.Now().After(deadline) {
					t.Fatalf("no report; stdout %q stderr %q", stdout.String(), stderr.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			cmd.Process.Signal(syscall.SIGHUP)
			err := cmd.Wait()
			if tc.want == 0 {
				if err != nil {
					t.Fatalf("ended with %v, stderr %q", err, stderr.String())
				}
				return
			}
			if ws, ok := exitStatus(err); !ok || !ws.Signaled() || ws.Signal() != tc.want {
				t.Fatalf("ended with %v, want death by %v", err, tc.want)
			}
		})
	}
}

package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// restoreWaitingForLock builds sc, snapshots a file, changes it, holds the
// database write lock and starts "sc restore" with the given command
// prefix. It returns the running command, its stderr, the file path and a
// function that releases the lock.
func restoreWaitingForLock(t *testing.T, prefix ...string) (*exec.Cmd, *strings.Builder, string, func()) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sc")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	env := append(os.Environ(), "SC_HOME="+home)
	sc := func(args ...string) string {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sc %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "conf")
	os.WriteFile(p, []byte("good\n"), 0o644)
	sc("init")
	id := sc("snapshot", "-q", p)
	os.WriteFile(p, []byte("bad\n"), 0o644)

	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "changes.db"))
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
	released := false
	release := func() {
		if !released {
			released = true
			conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			db.Close()
		}
	}
	t.Cleanup(release)

	args := append(append([]string{}, prefix...), bin, "restore", id)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if tmps, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*")); len(tmps) == 1 {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("restore never prepared its temp file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cmd, &stderr, p, release
}

func checkStoppedCleanly(t *testing.T, cmd *exec.Cmd, stderr *strings.Builder, p string) {
	t.Helper()
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit: %v, stderr %q", err, stderr.String())
	}
	msg := stderr.String()
	if strings.Count(msg, "\n") != 1 || !strings.Contains(msg, "interrupted") || !strings.Contains(msg, "file not changed") {
		t.Fatalf("stderr: %q", msg)
	}
	if tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".conf.sc-tmp-*")); len(tmps) != 0 {
		t.Fatalf("temp file left after interrupt: %v", tmps)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Fatalf("target changed by an interrupted restore: %q", b)
	}
}

// Ctrl-C while a restore waits for the database: it stops (within one busy
// timeout), leaves no temp file, and says the file was not changed.
func TestInterruptedRestoreRemovesTempFile(t *testing.T) {
	cmd, stderr, p, _ := restoreWaitingForLock(t)
	cmd.Process.Signal(syscall.SIGINT)
	checkStoppedCleanly(t, cmd, stderr, p)
}

// SIGQUIT (Ctrl-\) is handled the same way: one line, no stack trace.
func TestSIGQUITPrintsNoStackTrace(t *testing.T) {
	cmd, stderr, p, _ := restoreWaitingForLock(t)
	cmd.Process.Signal(syscall.SIGQUIT)
	checkStoppedCleanly(t, cmd, stderr, p)
	if strings.Contains(stderr.String(), "goroutine") {
		t.Fatalf("stack trace printed: %q", stderr.String())
	}
}

// A SIGINT the caller chose to ignore stays ignored: the restore finishes.
func TestIgnoredSIGINTStaysIgnored(t *testing.T) {
	cmd, stderr, p, release := restoreWaitingForLock(t, "sh", "-c", `trap "" INT; exec "$@"`, "sh")
	cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(300 * time.Millisecond)
	release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("restore stopped by an ignored SIGINT: %v %q", err, stderr.String())
	}
	if b, _ := os.ReadFile(p); string(b) != "good\n" {
		t.Fatalf("not restored: %q", b)
	}
}

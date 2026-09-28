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

// Interrupting sc restore while it waits for the database must not leave its
// prepared temp file (a full copy of the snapshot) in the config directory.
func TestInterruptedRestoreRemovesTempFile(t *testing.T) {
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

	// Hold the write lock so the restore waits after preparing its temp file.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "changes.db"))
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
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")

	cmd := exec.Command(bin, "restore", id)
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
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
	cmd.Process.Signal(syscall.SIGINT)
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit: %v", err)
	}
	if msg := stderr.String(); strings.Count(msg, "\n") != 1 || !strings.Contains(msg, "interrupted") {
		t.Fatalf("stderr: %q", msg)
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, ".conf.sc-tmp-*")); len(tmps) != 0 {
		t.Fatalf("temp file left after interrupt: %v", tmps)
	}
	if b, _ := os.ReadFile(p); string(b) != "bad\n" {
		t.Fatalf("target changed by an interrupted restore: %q", b)
	}
}

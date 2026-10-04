package main

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHotJournalChild is the process readOnlyHotStore kills: it commits
// SC_TEST_ROWS rows of path /x straight into the database, then fills a
// transaction past a one-page cache (so pages spill into the file) and
// kills itself, leaving a hot journal.
func TestHotJournalChild(t *testing.T) {
	db := os.Getenv("SC_TEST_HOTJOURNAL")
	if db == "" {
		t.Skip("run by readOnlyHotStore")
	}
	n, _ := strconv.Atoi(os.Getenv("SC_TEST_ROWS"))
	conn, err := sql.Open("sqlite", "file:"+db+"?_pragma=cache_size(1)")
	if err != nil {
		t.Fatal(err)
	}
	insert := func(tx *sql.Tx, id, intent string) {
		if _, err := tx.Exec(`INSERT INTO changes (id, ts, path, blob, size, mode, uid, gid, origin, intent)
			VALUES (?, 0, '/x', '', 0, 420, 0, 0, 'auto', ?)`, id, intent); err != nil {
			t.Fatal(err)
		}
	}
	tx, _ := conn.Begin()
	for i := 0; i < n; i++ {
		insert(tx, fmt.Sprintf("c%05x", i), "committed")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, _ = conn.Begin()
	for i := 0; i < 400; i++ {
		insert(tx, fmt.Sprintf("d%05x", i), strings.Repeat("y", 3000))
	}
	syscall.Kill(os.Getpid(), syscall.SIGKILL)
}

// readOnlyHotStore makes $SC_HOME a store with rows committed rows of /x
// and a hot journal, that this user may not write, and points the
// repaired copies at a directory of the test, which it returns.
func readOnlyHotStore(t *testing.T, rows int) (home, copies string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes through file modes (store's TestReadOnlyMount covers root)")
	}
	home = filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	mustSC(t, "init")
	db := filepath.Join(home, "changes.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHotJournalChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SC_TEST_HOTJOURNAL="+db, "SC_TEST_ROWS="+strconv.Itoa(rows))
	cmd.Run() // killed by itself
	if fi, err := os.Stat(db + "-journal"); err != nil || fi.Size() == 0 {
		t.Fatalf("no hot journal: %v", err)
	}
	os.Chmod(home, 0o500)
	t.Cleanup(func() { os.Chmod(home, 0o700) })
	copies = t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", copies)
	return home, copies
}

const copyNote = "sc: note: the store has a write a crash left unfinished; sc reads a repaired copy, and the store itself is repaired by the next sc run on a writable root\n"

// The rescue shell with a store a crash left half-written: sc log reads
// the committed rows from a copy, says so in one line, and removes it.
func TestReadOnlyCopyCLI(t *testing.T) {
	_, copies := readOnlyHotStore(t, 3)
	r := sc(t, "log", "/x")
	if r.code != 0 || strings.Count(r.stdout, "\n") != 4 || !strings.Contains(r.stdout, "c00002") || r.stderr != copyNote {
		t.Fatalf("%+v", r)
	}
	if left, _ := os.ReadDir(copies); len(left) != 0 {
		t.Errorf("copy left: %v", left)
	}
}

// A reader that goes away (sc log | head) or a stop signal ends sc as
// usual, and its repaired copy of the store is gone (the chunk A review:
// every such run left a full copy in /run).
func TestReadOnlyCopyRemovedOnSignal(t *testing.T) {
	_, copies := readOnlyHotStore(t, 3000)
	bin := scBinary(t)
	// The SIGPIPE disposition is set for sc: a caller may ignore it (this
	// test's own shell may), and sc keeps what it inherits.
	start := func(pipe string) (*exec.Cmd, *os.File, *lockedBuffer) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr lockedBuffer
		cmd := exec.Command("env", "--"+pipe+"-signal=PIPE", bin, "log", "-n", "0", "/x")
		cmd.Stdout, cmd.Stderr = w, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		w.Close()
		return cmd, r, &stderr
	}
	gone := func(what string) {
		if left, _ := os.ReadDir(copies); len(left) != 0 {
			t.Errorf("%s: copy left: %v", what, left)
		}
	}

	// sc log | head -1: sc exits 1 without a word, whether its caller
	// ignores SIGPIPE or not.
	for _, pipe := range []string{"default", "ignore"} {
		cmd, r, stderr := start(pipe)
		io.ReadFull(r, make([]byte, 100))
		r.Close()
		ws, _ := exitStatus(cmd.Wait())
		if !ws.Exited() || ws.ExitStatus() != 1 || stderr.String() != copyNote {
			t.Errorf("broken pipe, %s SIGPIPE: %v, stderr %q", pipe, ws, stderr.String())
		}
		gone("broken pipe, " + pipe + " SIGPIPE")
	}

	// A full pipe nobody reads, then SIGTERM.
	cmd, r, stderr := start("default")
	defer r.Close()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if left, _ := os.ReadDir(copies); len(left) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no copy made")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // writing, blocked on the pipe
	cmd.Process.Signal(syscall.SIGTERM)
	ws, _ := exitStatus(cmd.Wait())
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM || stderr.String() != copyNote+"sc: interrupted by terminated\n" {
		t.Errorf("SIGTERM: %v, stderr %q", ws, stderr.String())
	}
	gone("SIGTERM")
}

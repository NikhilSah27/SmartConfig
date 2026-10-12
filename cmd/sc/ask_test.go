package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A terminal is a file TCGETS works on: not a buffer, a pipe or /dev/null
// (a character device, which a mode test alone would take for one).
func TestIsTerminal(t *testing.T) {
	var b bytes.Buffer
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	for name, v := range map[string]any{"a buffer": &b, "a pipe": r, "/dev/null": null, "nil": (*os.File)(nil)} {
		if isTerminal(v) {
			t.Errorf("%s is called a terminal", name)
		}
	}
	// A pseudo-terminal's master answers TCGETS.
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer ptmx.Close()
	if !isTerminal(ptmx) {
		t.Error("/dev/ptmx is not called a terminal")
	}
}

// Without a terminal, sc restore does not ask and does not read stdin: a
// script's next line is not taken for an answer, and a prompt does not
// wait in a file. It lists what the version adds, says on stderr how to
// write it anyway, and exits 2 (the chunk J review, J6, your pick).
func TestRestoreNoTerminal(t *testing.T) {
	_, fstab := checkEnv(t)
	mustSC(t, "init")
	bad := snap(t, fstab, badLine)
	os.WriteFile(fstab, []byte(goodLine), 0o644)
	in := strings.NewReader("y\nthe next line of a script\n")
	old, oldTerm := restoreStdin, atTerminal
	restoreStdin, atTerminal = in, func(io.Reader, io.Writer) bool { return false }
	t.Cleanup(func() { restoreStdin, atTerminal = old, oldTerm })
	r := sc(t, "restore", bad)
	if r.code != 2 || !strings.Contains(r.stdout, "fstab-source-missing") ||
		!strings.HasSuffix(r.stdout, "\nRestoring "+bad+" adds 1 blocker compared with "+fstab+" as it is now.\n") ||
		r.stderr != "sc: not restored: "+fstab+" is unchanged (no terminal to ask at; sc restore --force "+bad+" writes it)\n" {
		t.Errorf("%+v", r)
	}
	if in.Len() != len("y\nthe next line of a script\n") {
		t.Errorf("stdin was read: %d bytes left", in.Len())
	}
	if b, _ := os.ReadFile(fstab); string(b) != goodLine {
		t.Errorf("restored: %q", b)
	}
	// What adds nothing needs no question, and restores as before.
	good := snap(t, fstab, goodLine)
	os.WriteFile(fstab, []byte(badLine), 0o644)
	if r := sc(t, "restore", good); r.code != 0 || r.stderr != "" || !strings.HasPrefix(r.stdout, "restored ") {
		t.Errorf("a version that adds nothing: %+v", r)
	}
}

// sc prune, the same: without a terminal it deletes nothing, reads
// nothing, and names --yes (the chunk J review, J6, your pick).
func TestPruneNoTerminal(t *testing.T) {
	_, p, oldID := pruneEnv(t)
	pruneAt(t, time.Now().Add(100*24*time.Hour), "y\n")
	in := pruneStdin.(*strings.Reader)
	oldTerm := atTerminal
	atTerminal = func(io.Reader, io.Writer) bool { return false }
	t.Cleanup(func() { atTerminal = oldTerm })
	r := sc(t, "prune")
	if r.code != 2 || !strings.HasPrefix(r.stdout, "1 version recorded by scd and replaced before ") || strings.Contains(r.stdout, "Delete them?") ||
		r.stderr != "sc: nothing deleted (no terminal to ask at; sc prune --yes deletes them)\n" {
		t.Errorf("%+v", r)
	}
	if in.Len() != 2 {
		t.Errorf("stdin was read: %d bytes left", in.Len())
	}
	if !strings.Contains(mustSC(t, "log", p), oldID) {
		t.Error("the old row was deleted")
	}
	if r := sc(t, "prune", "--yes"); r.code != 0 || !strings.HasSuffix(r.stdout, "deleted 1 version and 4 bytes of stored content\n") {
		t.Errorf("--yes: %+v", r)
	}
}

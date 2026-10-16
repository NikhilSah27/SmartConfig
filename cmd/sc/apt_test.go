package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/aptrun"
)

// sc apt begin marks the run of the process above it, live; end removes
// the mark, and is no error without one. Neither opens the store. Root
// only (M5 follow-ups, step 8).
func TestAptBeginEnd(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	mark := filepath.Join(t.TempDir(), "run", "smartconfig", "apt-run")
	old := aptRunFile
	aptRunFile = mark
	t.Cleanup(func() { aptRunFile = old })

	euidEnv(t, 1000)
	for _, a := range []string{"begin", "end"} {
		if r := sc(t, "apt", a); r.code != 1 || r.stderr != "sc: sc apt needs root\n" {
			t.Errorf("apt %s as a user: %+v", a, r)
		}
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("a user's begin wrote the mark")
	}

	euidEnv(t, 0)
	if r := sc(t, "apt", "begin"); r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("begin: %+v", r)
	}
	// The mark names this test's parent (go test), not computed with
	// Runner itself, unless that parent is a shell Runner climbs past.
	m, ok := aptrun.Read(mark)
	if comm, _, _, _ := aptrun.Stat(os.Getppid()); !ok || comm != "sh" && comm != "bash" && comm != "dash" && m.PID != os.Getppid() {
		t.Errorf("the mark: %+v %v, want pid %d (%s)", m, ok, os.Getppid(), comm)
	}
	if time.Since(m.Begun) > time.Minute || time.Since(m.Begun) < 0 {
		t.Errorf("begun %v", m.Begun)
	}
	// Only root can write the mark or its directory: a user who could
	// would tag rows at will.
	for p, want := range map[string]os.FileMode{mark: 0o644, filepath.Dir(mark): 0o755 | os.ModeDir} {
		if fi, err := os.Stat(p); err != nil || fi.Mode() != want {
			t.Errorf("%s: %v %v, want %v", p, fi.Mode(), err, want)
		}
	}
	if r := sc(t, "apt", "end"); r.code != 0 || r.stderr != "" {
		t.Errorf("end: %+v", r)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("end left the mark")
	}
	if r := sc(t, "apt", "end"); r.code != 0 {
		t.Errorf("end with no mark: %+v", r)
	}
	if r := sc(t, "apt", "middle"); r.code != 1 {
		t.Errorf("apt middle: %+v", r)
	}
	// No apt found: an error, and no mark.
	old2 := aptRunner
	aptRunner = func(int) (int, uint64, error) { return 0, 0, errors.New("no apt above a chain of shells") }
	defer func() { aptRunner = old2 }()
	if r := sc(t, "apt", "begin"); r.code != 1 || !strings.Contains(r.stderr, "no apt above") {
		t.Errorf("begin with no apt: %+v", r)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("a mark with no apt")
	}
	if _, err := os.Stat(home); err == nil {
		t.Error("sc apt made the store")
	}
}

// Only the system's scd reads the mark.
func TestAptRunFor(t *testing.T) {
	if aptRunFor(nil) != aptRunFile || aptRunFor([]string{"/x"}) != "" {
		t.Errorf("%q %q", aptRunFor(nil), aptRunFor([]string{"/x"}))
	}
}

// The package's apt hook (scripts/50smartconfig): each line runs sc apt
// with the word it should, and apt goes on (exit 0), with nothing of sc's
// in its output, when sc is missing, fails or prints (the chunk L
// review, L3). The lines run here with /usr/sbin/sc swapped for a made-up sc.
func TestAptHookLines(t *testing.T) {
	b, err := os.ReadFile("../../scripts/50smartconfig")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^DPkg::(Pre|Post)-Invoke \{ "([^"]*)"; \};$`)
	ms := re.FindAllStringSubmatch(string(b), -1)
	if len(ms) != 2 || ms[0][1] != "Pre" || ms[1][1] != "Post" {
		t.Fatalf("hook lines: %q", ms)
	}
	dir := t.TempDir()
	fake, said := filepath.Join(dir, "sc"), filepath.Join(dir, "said")
	for _, c := range []struct {
		sc   string // the made-up sc's script, "" for none
		want string // what it was called with
	}{
		{"#!/bin/sh\necho \"$@\" >>" + said + "\n", "apt begin\napt end\n"},
		// A failing sc, or one too old to know sc apt (0.5.0, kept by a
		// downgrade with this conffile): it says so, apt must not.
		{"#!/bin/sh\necho \"$@\" >>" + said + "\necho 'sc: unknown command \"apt\" for \"sc\"' >&2\nexit 3\n", "apt begin\napt end\n"},
		{"#!/bin/sh\necho \"$@\" >>" + said + "\necho 'out'\n", "apt begin\napt end\n"},
		{"", ""},
	} {
		os.Remove(fake)
		os.Remove(said)
		if c.sc != "" {
			os.WriteFile(fake, []byte(c.sc), 0o755)
		}
		for _, m := range ms {
			line := strings.ReplaceAll(m[2], "/usr/sbin/sc", fake)
			// Nothing said either: apt prints what its hooks print.
			if out, err := exec.Command("sh", "-c", line).CombinedOutput(); err != nil || len(out) > 0 {
				t.Errorf("%s with sc %q: %v %q", m[1], c.sc, err, out)
			}
		}
		if got, _ := os.ReadFile(said); string(got) != c.want {
			t.Errorf("sc %q was called with %q", c.sc, got)
		}
	}
}

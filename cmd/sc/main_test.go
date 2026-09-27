package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type result struct {
	code           int
	stdout, stderr string
}

func sc(t *testing.T, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return result{code, out.String(), errb.String()}
}

func mustSC(t *testing.T, args ...string) string {
	t.Helper()
	r := sc(t, args...)
	if r.code != 0 {
		t.Fatalf("sc %v: exit %d, stderr %q", args, r.code, r.stderr)
	}
	return r.stdout
}

func TestCLI(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	dir := t.TempDir()
	p := filepath.Join(dir, "hosts")
	os.WriteFile(p, []byte("127.0.0.1 localhost\n"), 0o600)

	if r := sc(t, "snapshot", p); r.code != 1 || !strings.Contains(r.stderr, "sc init") {
		t.Fatalf("before init: %+v", r)
	}
	if out := mustSC(t, "init"); out != "initialised "+home+"\n" {
		t.Fatalf("init: %q", out)
	}
	mustSC(t, "init")

	id1 := strings.TrimSpace(mustSC(t, "snapshot", p, "-q", "-m", "baseline"))
	if len(id1) != 6 {
		t.Fatalf("quiet id %q", id1)
	}
	if out := mustSC(t, "snapshot", p); out != "unchanged since "+id1+"\n" {
		t.Fatalf("unchanged: %q", out)
	}

	os.WriteFile(p, []byte("127.0.0.1 localhost\n127.0.0.1 smoke.invalid\n"), 0o600)
	out := mustSC(t, "snapshot", p, "-m", "added smoke line")
	if !strings.HasPrefix(out, "snapshot ") || !strings.HasSuffix(out, "  "+p+"  (44 bytes)\n") {
		t.Fatalf("snapshot: %q", out)
	}
	id2 := strings.Fields(out)[1]

	if out := mustSC(t, "cat", id1); out != "127.0.0.1 localhost\n" {
		t.Fatalf("cat: %q", out)
	}
	out = mustSC(t, "diff", id1)
	for _, want := range []string{
		"--- a" + p + " (snapshot " + id1 + ")\n",
		"+++ b" + p + " (on disk)\n",
		"\n+127.0.0.1 smoke.invalid\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff missing %q:\n%s", want, out)
		}
	}
	if out := mustSC(t, "diff", id2); out != "no differences\n" {
		t.Fatalf("diff identical: %q", out)
	}
	if out := mustSC(t, "diff", id1, id2); !strings.Contains(out, "+++ b"+p+" (snapshot "+id2+")") {
		t.Fatalf("diff two ids:\n%s", out)
	}

	out = mustSC(t, "restore", id1[:3])
	if !strings.HasPrefix(out, "restored "+p+" from "+id1+" (mode 0600 ") ||
		!strings.Contains(out, "), previous state saved as ") {
		t.Fatalf("restore: %q", out)
	}
	if b, _ := os.ReadFile(p); string(b) != "127.0.0.1 localhost\n" {
		t.Fatalf("restored content %q", b)
	}

	out = mustSC(t, "log", p)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "ID  ") || !strings.Contains(lines[0], "WHAT") {
		t.Fatalf("log:\n%s", out)
	}
	for i, origin := range []string{"restore", "pre-restore", "manual", "manual"} {
		if f := strings.Fields(lines[i+1]); f[3] != origin {
			t.Fatalf("log row %d origin %q, want %q:\n%s", i, f[3], origin, out)
		}
	}
	if !strings.Contains(lines[1], "restored from "+id1) || !strings.Contains(lines[4], "baseline") {
		t.Fatalf("log intents:\n%s", out)
	}
	if out := mustSC(t, "log", "-n", "1"); len(strings.Split(strings.TrimSpace(out), "\n")) != 2 {
		t.Fatalf("log -n 1:\n%s", out)
	}
}

func TestCLIErrorsAreOneLine(t *testing.T) {
	t.Setenv("SC_HOME", t.TempDir())
	mustSC(t, "init")
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	os.Symlink("/etc/hosts", link)
	for _, args := range [][]string{
		{"snapshot", link},
		{"snapshot", filepath.Join(dir, "missing")},
		{"cat", "zzz"},
		{"cat", "abcdef"},
		{"restore"},
		{"bogus"},
	} {
		r := sc(t, args...)
		if r.code != 1 || r.stdout != "" || !strings.HasPrefix(r.stderr, "sc: ") ||
			strings.Count(r.stderr, "\n") != 1 {
			t.Errorf("sc %v: %+v", args, r)
		}
	}
}

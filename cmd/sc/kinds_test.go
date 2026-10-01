package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// kindsHome makes an SC_HOME and a work dir; record writes watcher rows
// into it directly, as scd would.
func kindsHome(t *testing.T) (dir string, record func(p string, created bool) store.Change) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	mustSC(t, "init")
	dir = t.TempDir()
	return dir, func(p string, created bool) store.Change {
		t.Helper()
		s, err := store.Open(home)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		o := store.Obs{Path: p, Origin: store.OriginAuto, Created: created}
		if st, err := fsutil.ReadState(p); err == nil {
			o.State = &st
		}
		res, err := s.Record([]store.Obs{o})
		if err != nil {
			t.Fatal(err)
		}
		return res[0].Change
	}
}

// logFields returns sc log's rows (without the header) split on spaces.
func logFields(t *testing.T, args ...string) [][]string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(mustSC(t, append([]string{"log"}, args...)...)), "\n")
	var rows [][]string
	for _, l := range lines[1:] {
		rows = append(rows, strings.Fields(l))
	}
	return rows
}

func TestCLILinks(t *testing.T) {
	dir, _ := kindsHome(t)
	l := filepath.Join(dir, "localtime")
	os.Symlink("/usr/share/zoneinfo/Etc/UTC", l)
	out := mustSC(t, "snapshot", l)
	id := strings.Fields(out)[1]
	if out != "snapshot "+id+"  "+l+"  (link -> /usr/share/zoneinfo/Etc/UTC)\n" {
		t.Fatalf("snapshot: %q", out)
	}
	if out := mustSC(t, "cat", id); out != "/usr/share/zoneinfo/Etc/UTC\n" {
		t.Fatalf("cat: %q", out)
	}
	if out := mustSC(t, "diff", id); out != "no differences\n" {
		t.Fatalf("diff of an unchanged link: %q", out)
	}
	os.Remove(l)
	os.Symlink("/usr/share/zoneinfo/Asia/Kolkata", l)
	if out := mustSC(t, "diff", id); !strings.Contains(out, "-/usr/share/zoneinfo/Etc/UTC\n+/usr/share/zoneinfo/Asia/Kolkata\n") {
		t.Fatalf("diff: %q", out)
	}
	out = mustSC(t, "restore", id)
	if !strings.HasPrefix(out, "restored "+l+" from "+id+" (link -> /usr/share/zoneinfo/Etc/UTC, owner ") ||
		!strings.Contains(out, "), previous state saved as ") {
		t.Fatalf("restore: %q", out)
	}
	if got, _ := os.Readlink(l); got != "/usr/share/zoneinfo/Etc/UTC" {
		t.Fatalf("link -> %q", got)
	}
	// ORIGIN is still field 4 (after date and time), and SIZE says link.
	rows := logFields(t, l)
	for i, want := range []string{"restore", "pre-restore", "manual"} {
		if rows[i][3] != want || rows[i][5] != "link" {
			t.Fatalf("log row %d: %q", i, rows[i])
		}
	}
}

func TestCLIDeletions(t *testing.T) {
	dir, record := kindsHome(t)
	p := filepath.Join(dir, "x.service")
	os.WriteFile(p, []byte("[Unit]\n"), 0o644)
	record(p, true) // did not exist, created
	rows := logFields(t, p)
	if len(rows) != 2 || rows[1][3] != "auto" || rows[1][5] != "deleted" || rows[0][5] != "7" {
		t.Fatalf("log: %q", rows)
	}
	gone := rows[1][0]
	r := sc(t, "cat", gone)
	if r.code != 1 || r.stderr != "sc: "+gone+" records that "+p+" did not exist\n" {
		t.Fatalf("cat: %+v", r)
	}
	out := mustSC(t, "restore", gone)
	if !strings.HasPrefix(out, "removed "+p+" to match "+gone+" (did not exist), previous state saved as ") {
		t.Fatalf("restore: %q", out)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
	if out := mustSC(t, "restore", gone); out != "nothing to do: "+p+" is already absent, as in "+gone+"\n" {
		t.Fatalf("again: %q", out)
	}
	// A plain deletion says "deleted".
	q := filepath.Join(dir, "conf")
	os.WriteFile(q, []byte("x"), 0o644)
	record(q, false)
	os.Remove(q)
	del := record(q, false)
	os.WriteFile(q, []byte("y"), 0o644)
	if out := mustSC(t, "restore", del.ID); !strings.HasPrefix(out, "removed "+q+" to match "+del.ID+" (deleted), ") {
		t.Fatalf("restore deleted: %q", out)
	}
	if r := sc(t, "cat", del.ID); r.stderr != "sc: "+del.ID+" records that "+q+" was deleted\n" {
		t.Fatalf("cat deleted: %+v", r)
	}
}

func TestCLIFingerprints(t *testing.T) {
	dir, _ := kindsHome(t)
	key := filepath.Join(dir, "ssh_host_rsa_key")
	os.WriteFile(key, []byte("PRIVATE\n"), 0o600)
	// An M1-era file row of the key, written before the rule existed.
	oldRow := strings.Fields(mustSC(t, "snapshot", key))[1]
	testHookOpenStore = func(s *store.Store) { s.SetFingerprintOnly(func(p string) bool { return p == key }) }
	defer func() { testHookOpenStore = nil }()
	os.WriteFile(key, []byte("NEW PRIVATE\n"), 0o600)
	out := mustSC(t, "snapshot", key)
	digest := strings.Fields(out)[1]
	if out != "snapshot "+digest+"  "+key+"  (fingerprint only)\n" {
		t.Fatalf("snapshot: %q", out)
	}
	if rows := logFields(t, key); rows[0][5] != "digest" {
		t.Fatalf("log: %q", rows)
	}
	for _, args := range [][]string{{"cat", oldRow}, {"cat", digest}, {"diff", oldRow}, {"diff", digest},
		{"diff", oldRow, digest}, {"restore", oldRow}, {"restore", digest}} {
		r := sc(t, args...)
		if r.code != 1 || r.stdout != "" || !strings.HasPrefix(r.stderr, "sc: "+key+" is fingerprint-only; sc never ") ||
			strings.Count(r.stderr, "\n") != 1 || strings.Contains(r.stderr, "PRIVATE") {
			t.Errorf("sc %v: %+v", args, r)
		}
	}
	if b, _ := os.ReadFile(key); string(b) != "NEW PRIVATE\n" {
		t.Fatal("the key was touched")
	}
}

// A digest row of a path the rule does not cover (a user file over the
// size limit) is refused by cat, diff and restore, with one line.
func TestCLIDigestRow(t *testing.T) {
	dir, _ := kindsHome(t)
	home := os.Getenv("SC_HOME")
	u := filepath.Join(dir, "authorized_keys")
	os.WriteFile(u, []byte("ssh-ed25519 AAAA\n"), 0o600)
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := fsutil.ReadState(u)
	res, err := s.Record([]store.Obs{{Path: u, State: &st, Digest: true, Origin: store.OriginAuto}})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	id := res[0].Change.ID
	for _, args := range [][]string{{"cat", id}, {"diff", id}, {"restore", id}} {
		r := sc(t, args...)
		if r.code != 1 || r.stderr != "sc: "+id+" keeps only a fingerprint of "+u+", not its content\n" {
			t.Errorf("sc %v: %+v", args, r)
		}
	}
}

// A newline in a link target or name cannot forge output lines: such
// strings are quoted (chunk B review D3).
func TestCLIQuotesControlCharacters(t *testing.T) {
	dir, record := kindsHome(t)
	l := filepath.Join(dir, "nl")
	target := "/x\nsnapshot ffffff  /etc/shadow  (12 bytes)"
	os.Symlink(target, l)
	out := mustSC(t, "snapshot", l)
	id := strings.Fields(out)[1]
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `(link -> "/x\nsnapshot ffffff`) {
		t.Fatalf("snapshot: %q", out)
	}
	os.Remove(l)
	os.Symlink("/y", l)
	record(l, false)
	out = mustSC(t, "restore", id)
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `(link -> "/x\nsnapshot`) {
		t.Fatalf("restore: %q", out)
	}
	odd := filepath.Join(dir, "a\x1b[31mred")
	os.WriteFile(odd, []byte("x"), 0o644)
	mustSC(t, "snapshot", odd)
	out = mustSC(t, "log")
	if n := strings.Count(out, "\n"); n != 6 || strings.Contains(out, "\x1b") {
		t.Fatalf("log (%d lines): %q", n, out)
	}
	// Ordinary names are printed as they are.
	plain := filepath.Join(dir, "plain file")
	os.WriteFile(plain, []byte("x"), 0o644)
	if out := mustSC(t, "snapshot", plain); !strings.Contains(out, "  "+plain+"  (1 bytes)") {
		t.Fatalf("plain: %q", out)
	}
}

// The link restore line names the link's owner and group (root half: a
// group that differs from the owner).
func TestCLILinkRestoreOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir, _ := kindsHome(t)
	l := filepath.Join(dir, "unit")
	os.Symlink("/nonexistent", l)
	os.Lchown(l, 0, 65534)
	id := strings.Fields(mustSC(t, "snapshot", l))[1]
	os.Remove(l)
	os.Symlink("/other", l)
	out := mustSC(t, "restore", id)
	if !strings.Contains(out, "(link -> /nonexistent, owner root:"+groupName(65534)+")") || groupName(65534) == "root" {
		t.Fatalf("restore: %q", out)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smartconfig/internal/check"
	"smartconfig/internal/scope"
)

// secretEnv makes the files named shadow and *.nmconnection secret files,
// whatever their directory (chunk K, step 6).
func secretEnv(t *testing.T) {
	t.Helper()
	secretFile = func(p string) bool {
		return filepath.Base(p) == "shadow" || strings.HasSuffix(p, ".nmconnection") || strings.HasSuffix(p, "/fstab")
	}
	t.Cleanup(func() {
		secretFile = func(p string) bool { return scope.Default().Secret(p) }
	})
}

// sc cat and sc diff show a secret file's secret values as markers, and
// say so on stderr; other files are shown exactly. Restore still writes
// the real content.
func TestCatDiffSecrets(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	secretEnv(t)
	mustSC(t, "init")
	dir := t.TempDir()
	shadow := filepath.Join(dir, "shadow")
	const v1 = "root:$6$salt$hunter22:19000:0:99999:7:::\nbob:!$6$s2$pw2bob:19000::::::\ndaemon:*:19000::::::\n"
	os.WriteFile(shadow, []byte(v1), 0o600)
	id1 := snapID(t, shadow)

	r := sc(t, "cat", id1)
	want := "root:<hidden>:19000:0:99999:7:::\nbob:!<hidden>:19000::::::\ndaemon:*:19000::::::\n"
	if r.code != 0 || r.stdout != want || r.stderr != "sc: note: the secret values of "+shadow+" are shown as <hidden> (as root, --show-secrets shows them)\n" {
		t.Fatalf("cat: %+v", r)
	}

	// A plain file next to it is shown exactly, with no note.
	plain := filepath.Join(dir, "hosts")
	os.WriteFile(plain, []byte("password=x\n"), 0o644)
	if r := sc(t, "cat", snapID(t, plain)); r.stdout != "password=x\n" || r.stderr != "" {
		t.Errorf("plain cat: %+v", r)
	}

	// Against the disk: a changed password is a changed marker; the
	// unchanged one keeps its number on both sides.
	os.WriteFile(shadow, []byte(strings.Replace(v1, "hunter22", "newpass9", 1)), 0o600)
	r = sc(t, "diff", id1)
	if r.code != 0 || !strings.Contains(r.stdout, "-root:<hidden 1>:19000") || !strings.Contains(r.stdout, "+root:<hidden 3>:19000") ||
		r.stderr != "sc: note: the secret values of "+shadow+" are shown as <hidden N> (as root, --show-secrets shows them)\n" {
		t.Fatalf("diff: %+v", r)
	}
	id2 := snapID(t, shadow)
	if r := sc(t, "diff", id1, id2); !strings.Contains(r.stdout, "-root:<hidden 1>") || !strings.Contains(r.stdout, "+root:<hidden 3>") {
		t.Errorf("diff of two: %+v", r)
	}
	if r := sc(t, "diff", id2); r.stdout != "no differences\n" {
		t.Errorf("same: %+v", r)
	}

	// A blank after a hidden value is kept, so a change in it shows.
	nm := filepath.Join(dir, "home.nmconnection")
	os.WriteFile(nm, []byte("[wifi-security]\npsk=hunter22\n"), 0o600)
	idNM := snapID(t, nm)
	os.WriteFile(nm, []byte("[wifi-security]\npsk=hunter22 \n"), 0o600)
	if r := sc(t, "diff", idNM); !strings.Contains(r.stdout, "\n-psk=<hidden 1>\n+psk=<hidden 1> \n") {
		t.Errorf("blanks: %+v", r)
	}

	for _, out := range []string{mustSC(t, "cat", id2), mustSC(t, "diff", id1, id2), mustSC(t, "cat", idNM)} {
		if strings.Contains(out, "hunter22") || strings.Contains(out, "newpass9") || strings.Contains(out, "pw2bob") {
			t.Errorf("a secret shown: %q", out)
		}
	}

	// Restore writes what was stored, not what was shown.
	mustSC(t, "restore", "--force", id1)
	if b, _ := os.ReadFile(shadow); string(b) != v1 {
		t.Errorf("restored %q", b)
	}
}

// snapID snapshots p and returns the new row's id.
func snapID(t *testing.T, p string) string {
	t.Helper()
	f := strings.Fields(mustSC(t, "snapshot", p))
	if len(f) < 2 || f[0] != "snapshot" {
		t.Fatalf("snapshot %s: %q", p, f)
	}
	return f[1]
}

// sc check -v and sc edit hide a secret file's values in what a validator
// said about it: a key=value it quotes, and a value it quotes alone.
func TestCheckEditSaidSecrets(t *testing.T) {
	_, fstab := editEnv(t, "", "//srv/s /mnt cifs username=bob,password=hunter22,ro 0 0\n")
	secretEnv(t)
	os.WriteFile(fstab, []byte("//srv/s /mnt cifs username=bob,password=hunter22 0 0\n"), 0o600)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\necho \"findmnt: what is 'hunter22' in //srv/s /mnt cifs username=bob,password=hunter22\" >&2\nexit 1\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
	for _, args := range [][]string{{"check", "-v", fstab}, {"edit", fstab}} {
		r := sc(t, args...)
		if strings.Contains(r.stdout+r.stderr, "hunter22") || !strings.Contains(r.stdout, "username=bob,password=<hidden>") ||
			!strings.Contains(r.stdout, "what is '<hidden>' in") {
			t.Errorf("sc %v: %+v", args, r)
		}
	}
}

// sc scope says which files are secret, and by which line.
func TestScopeSecretLine(t *testing.T) {
	t.Setenv("SC_HOME", "/var/lib/smartconfig")
	r := sc(t, "scope", "/etc/shadow", "/etc/passwd")
	blocks := strings.Split(r.stdout, "\n\n")
	if r.code != 0 || len(blocks) != 2 ||
		!strings.Contains(blocks[0], "\n  content:  stored; shown with its secret values hidden, scope line ") ||
		!strings.Contains(blocks[0], "\n            secret /etc/{shadow,gshadow}\n") || strings.Contains(blocks[1], "content:") {
		t.Errorf("%+v", r)
	}
}

// euidEnv makes sc see uid as its effective uid (chunk K, step 7).
func euidEnv(t *testing.T, uid int) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = old })
}

// --show-secrets: as anyone but root it is refused before anything is
// read; as root, sc cat, sc diff and sc check -v show the secret values as
// stored, with no note. The next run without it hides them again.
func TestShowSecrets(t *testing.T) {
	_, fstab := editEnv(t, "", "//srv/s /mnt cifs username=bob,password=hunter22,ro 0 0\n")
	secretEnv(t)
	shadow := filepath.Join(t.TempDir(), "shadow")
	const v1 = "root:$6$salt$hunter22:19000:0:99999:7:::\n"
	os.WriteFile(shadow, []byte(v1), 0o600)
	id1 := snapID(t, shadow)
	os.WriteFile(shadow, []byte(strings.Replace(v1, "hunter22", "newpass9", 1)), 0o600)
	os.WriteFile(fstab, []byte("//srv/s /mnt cifs username=bob,password=hunter22 0 0\n"), 0o600)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\necho \"findmnt: bad: username=bob,password=hunter22\" >&2\nexit 1\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }

	euidEnv(t, 1000)
	for _, args := range [][]string{{"cat", "--show-secrets", id1}, {"diff", "--show-secrets", id1}, {"check", "-v", "--show-secrets", fstab}} {
		if r := sc(t, args...); r.code != 1 || r.stdout != "" || r.stderr != "sc: --show-secrets needs root\n" {
			t.Errorf("sc %v as a user: %+v", args, r)
		}
	}

	euidEnv(t, 0)
	if r := sc(t, "cat", "--show-secrets", id1); r.code != 0 || r.stdout != v1 || r.stderr != "" {
		t.Errorf("cat: %+v", r)
	}
	if r := sc(t, "diff", "--show-secrets", id1); r.code != 0 || r.stderr != "" ||
		!strings.Contains(r.stdout, "-root:$6$salt$hunter22:") || !strings.Contains(r.stdout, "+root:$6$salt$newpass9:") {
		t.Errorf("diff: %+v", r)
	}
	if r := sc(t, "check", "-v", "--show-secrets", fstab); !strings.Contains(r.stdout, "username=bob,password=hunter22") {
		t.Errorf("check: %+v", r)
	}

	// The flag does not outlive its run.
	if r := sc(t, "cat", id1); strings.Contains(r.stdout, "hunter22") || !strings.Contains(r.stdout, "root:<hidden>:") {
		t.Errorf("cat after: %+v", r)
	}
	if r := sc(t, "check", "-v", fstab); strings.Contains(r.stdout+r.stderr, "hunter22") {
		t.Errorf("check after: %+v", r)
	}
}

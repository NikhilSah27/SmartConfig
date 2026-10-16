package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"smartconfig/internal/check"
)

// secretEnv makes the files named shadow, *.nmconnection and fstab secret
// files, and *.secret files hidden whole, whatever their directory (chunk
// K, step 6). It puts the real rule back after t (the review's K11).
func secretEnv(t *testing.T) {
	t.Helper()
	old := secretFile
	secretFile = func(p string) secrecy {
		switch {
		case strings.HasSuffix(p, ".secret"):
			return secretWhole
		case filepath.Base(p) == "shadow" || strings.HasSuffix(p, ".nmconnection") || strings.HasSuffix(p, "/fstab"):
			return secretValues
		}
		return notSecret
	}
	t.Cleanup(func() { secretFile = old })
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

	// Against the disk: a changed password is marked old and new; the
	// unchanged one is <hidden> on both sides.
	os.WriteFile(shadow, []byte(strings.Replace(v1, "hunter22", "newpass9", 1)), 0o600)
	r = sc(t, "diff", id1)
	if r.code != 0 || !strings.Contains(r.stdout, "\n-root:<hidden, old>:19000:0:99999:7:::\n+root:<hidden, new>:19000:0:99999:7:::\n bob:!<hidden>:19000::::::\n") ||
		r.stderr != "sc: note: the secret values of "+shadow+" are shown as <hidden>, <hidden, old> or <hidden, new> (as root, --show-secrets shows them)\n" {
		t.Fatalf("diff: %+v", r)
	}
	id2 := snapID(t, shadow)
	if r := sc(t, "diff", id1, id2); !strings.Contains(r.stdout, "-root:<hidden, old>") || !strings.Contains(r.stdout, "+root:<hidden, new>") {
		t.Errorf("diff of two: %+v", r)
	}
	if r := sc(t, "diff", id2); r.stdout != "no differences\n" {
		t.Errorf("same: %+v", r)
	}

	// A blank after a hidden value is kept, so a change in it shows.
	nm := filepath.Join(dir, "home.nmconnection")
	os.WriteFile(nm, []byte("[wifi-security]\nwep-key0=hunter22\n"), 0o600)
	idNM := snapID(t, nm)
	os.WriteFile(nm, []byte("[wifi-security]\nwep-key0=hunter22 \n"), 0o600)
	if r := sc(t, "diff", idNM); !strings.Contains(r.stdout, "\n-wep-key0=<hidden>\n+wep-key0=<hidden> \n") {
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

// diffNote is sc diff's note for p.
func diffNote(p string) string {
	return "sc: note: the secret values of " + p + " are shown as <hidden>, <hidden, old> or <hidden, new> (as root, --show-secrets shows them)\n"
}

// K7: sc diff's markers tell a changed value from an unchanged one, and
// nothing more. Equal values on two lines look the same as different
// ones; a value one side does not hide is old or new; each side is
// hidden by its own path. K6: one note for each path that hid anything,
// the file on disk included.
func TestDiffSecretMarkers(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	secretEnv(t)
	mustSC(t, "init")
	dir := t.TempDir()

	// Two accounts with one hash, and two with different hashes: only
	// root's last-change day changes. The two diffs read the same.
	var outs []string
	for i, v := range []string{"root:$6$s$sekrit1:1::::::\nbob:$6$s$sekrit1:1::::::\n", "root:$6$s$sekrit1:1::::::\nbob:$6$s$sekrit2:1::::::\n"} {
		sh := filepath.Join(dir, strconv.Itoa(i), "shadow")
		os.MkdirAll(filepath.Dir(sh), 0o700)
		os.WriteFile(sh, []byte(v), 0o600)
		id := snapID(t, sh)
		os.WriteFile(sh, []byte(strings.Replace(v, ":1:", ":2:", 1)), 0o600)
		r := sc(t, "diff", id)
		if r.code != 0 || r.stderr != diffNote(sh) {
			t.Fatalf("diff %d: %+v", i, r)
		}
		_, hunks, _ := strings.Cut(r.stdout, "@@") // past the labels, whose ids differ
		outs = append(outs, hunks)
	}
	if outs[0] != outs[1] || !strings.Contains(outs[0], "\n-root:<hidden>:1::::::\n+root:<hidden>:2::::::\n bob:<hidden>:1::::::\n") {
		t.Errorf("one hash:\n%s\ntwo hashes:\n%s", outs[0], outs[1])
	}

	// Two accounts that swap hashes: every value is in both versions, so
	// nothing shown differs, but the bytes do.
	sh := filepath.Join(dir, "shadow")
	os.WriteFile(sh, []byte("root:$6$s$sekrit1:1::::::\nbob:$6$s$sekrit2:1::::::\n"), 0o600)
	id := snapID(t, sh)
	os.WriteFile(sh, []byte("root:$6$s$sekrit2:1::::::\nbob:$6$s$sekrit1:1::::::\n"), 0o600)
	if r := sc(t, "diff", id); r.stdout != "no differences shown: the two differ only where values are hidden\n" || r.stderr != diffNote(sh) {
		t.Errorf("swapped: %+v", r)
	}

	// A WEP key added on disk since the version: new, and the note says so
	// although the version hid nothing (K6).
	nm := filepath.Join(dir, "Home.nmconnection")
	os.WriteFile(nm, []byte("[wifi-security]\nkey-mgmt=none\n"), 0o600)
	idNM := snapID(t, nm)
	os.WriteFile(nm, []byte("[wifi-security]\nkey-mgmt=none\nwep-key0=sekrit\n"), 0o600)
	if r := sc(t, "diff", idNM); r.code != 0 || !strings.Contains(r.stdout, "\n+wep-key0=<hidden, new>\n") || r.stderr != diffNote(nm) {
		t.Errorf("added on disk: %+v", r)
	}
	// The other way round: removed since the version.
	idNM2 := snapID(t, nm)
	os.WriteFile(nm, []byte("[wifi-security]\nkey-mgmt=none\n"), 0o600)
	if r := sc(t, "diff", idNM2); !strings.Contains(r.stdout, "\n-wep-key0=<hidden, old>\n") || r.stderr != diffNote(nm) {
		t.Errorf("removed on disk: %+v", r)
	}

	// Two ids of different paths, a secret file and one that is not: each
	// side by its own path, a note for the secret one only. The plain
	// file's text is shown; its equal value does not make the secret's
	// <hidden>, as only hidden values count.
	plain := filepath.Join(dir, "hosts")
	os.WriteFile(plain, []byte("wep-key0=sekrit\n"), 0o644)
	idPlain := snapID(t, plain)
	if r := sc(t, "diff", idPlain, idNM2); !strings.Contains(r.stdout, "\n-wep-key0=sekrit\n") || !strings.Contains(r.stdout, "\n+wep-key0=<hidden, new>\n") || r.stderr != diffNote(nm) {
		t.Errorf("plain, secret: %+v", r)
	}
	if r := sc(t, "diff", idNM2, idPlain); !strings.Contains(r.stdout, "\n-wep-key0=<hidden, old>\n") || !strings.Contains(r.stdout, "\n+wep-key0=sekrit\n") || r.stderr != diffNote(nm) {
		t.Errorf("secret, plain: %+v", r)
	}

	// Two secret files of different paths with one WEP key: <hidden> on both
	// sides, and a note for each path.
	nm2 := filepath.Join(dir, "Work.nmconnection")
	os.WriteFile(nm2, []byte("[wifi-security]\nwep-key0=sekrit\n"), 0o600)
	if r := sc(t, "diff", idNM2, snapID(t, nm2)); !strings.Contains(r.stdout, "\n wep-key0=<hidden>\n") || strings.Contains(r.stdout, "sekrit") ||
		r.stderr != diffNote(nm)+diffNote(nm2) {
		t.Errorf("two secret files: %+v", r)
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
	r := sc(t, "scope", "/etc/shadow", "/etc/passwd", "/etc/ldap.secret")
	blocks := strings.Split(r.stdout, "\n\n")
	if r.code != 0 || len(blocks) != 3 ||
		!strings.Contains(blocks[0], "\n  content:  stored; shown with its secret values hidden, scope line ") ||
		!strings.Contains(blocks[0], "\n            secret /etc/*shadow*\n") || strings.Contains(blocks[1], "content:") ||
		!strings.Contains(blocks[2], "\n  content:  stored; shown hidden, every line of it, scope line ") ||
		!strings.Contains(blocks[2], "\n            hide /etc/*.secret{,s}\n") {
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

// The real rule (the review's K12, S1): the default scope's secret and
// hide lines, asked of the path as given and with its directories
// resolved, so a link to a secret directory is secret too.
func TestSecretFileRule(t *testing.T) {
	for p, want := range map[string]secrecy{"/etc/shadow": secretValues, "/etc/fstab": secretValues,
		"/etc/ldap.secret": secretWhole, "/etc/hosts": notSecret} {
		if got := secretFile(p); got != want {
			t.Errorf("secretFile(%s) = %d, want %d", p, got, want)
		}
	}
	link := filepath.Join(t.TempDir(), "np")
	if err := os.Symlink("/etc/netplan", link); err != nil {
		t.Fatal(err)
	}
	if got := secretFile(filepath.Join(link, "x.yaml")); got != secretValues {
		t.Errorf("through a link: %d", got)
	}
}

// A file that is only a secret is hidden whole, in sc cat and in what a
// validator said about it.
func TestCatWhole(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	secretEnv(t)
	mustSC(t, "init")
	p := filepath.Join(t.TempDir(), "ldap.secret")
	os.WriteFile(p, []byte("sekrit1\n\n  sekrit2 x\n"), 0o600)
	r := sc(t, "cat", snapID(t, p))
	if r.code != 0 || r.stdout != "<hidden>\n\n  <hidden>\n" || !strings.Contains(r.stderr, "shown as <hidden>") {
		t.Errorf("cat: %+v", r)
	}
	fs := []check.Finding{{Raw: "line 3: sekrit2 x"}}
	said := []string{"bad: sekrit1"}
	hideSaid(p, []byte("sekrit1\n\n  sekrit2 x\n"), fs, said)
	if fs[0].Raw != "line 3: <hidden>" || said[0] != "bad: <hidden>" {
		t.Errorf("said: %q %q", fs[0].Raw, said)
	}
}

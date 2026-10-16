package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smartconfig/internal/check"
)

// The tests below close the gaps the chunk K review's mutations found in
// sc's commands (K12). Their made-up values hold "sekrit", so a leak names
// its input.

// fakeFindmnt makes findmnt the shell script body, for the checks sc runs.
func fakeFindmnt(t *testing.T, body string) {
	t.Helper()
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\n"+body), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
}

// sc restore hides, in what a validator said about a secret file, the
// values of the version it is about to write: a value it quotes alone and
// a key=value.
func TestGapRestoreHidesSaid(t *testing.T) {
	_, fstab := checkEnv(t)
	secretEnv(t)
	mustSC(t, "init")
	id := snap(t, fstab, "//srv/s /mnt cifs username=bob,password=sekrit9 0 0\n")
	os.WriteFile(fstab, []byte("//srv/s /mnt cifs username=bob,password=goodpw1 0 0\n"), 0o600)
	fakeFindmnt(t, "for f; do :; done\ngrep -q sekrit9 \"$f\" || exit 0\necho /mnt\necho \"   [E] bad value 'sekrit9' in password=sekrit9\"\nexit 1\n")
	restoreAnswers(t, "")
	r := sc(t, "restore", id)
	if r.code != 2 || strings.Contains(r.stdout+r.stderr, "sekrit9") || !strings.Contains(r.stdout, "[E] bad value '<hidden>' in password=<hidden>") {
		t.Errorf("%+v", r)
	}
}

// sc edit takes the values to hide from the content after the edit: a
// password the edit put in is hidden where a validator quotes it.
func TestGapEditValuesAfter(t *testing.T) {
	_, fstab := editEnv(t, "", "//srv/s /mnt cifs username=bob,password=sekrit9 0 0\n")
	secretEnv(t)
	os.WriteFile(fstab, []byte("//srv/s /mnt cifs username=bob,password=oldpw11 0 0\n"), 0o600)
	fakeFindmnt(t, "echo \"findmnt: what is 'sekrit9'?\" >&2\nexit 1\n")
	r := sc(t, "edit", fstab)
	if strings.Contains(r.stdout+r.stderr, "sekrit9") || !strings.Contains(r.stdout, "findmnt: what is '<hidden>'?") {
		t.Errorf("%+v", r)
	}
}

// sc diff of two versions judges each side by its own path: a file with no
// secrets is shown as it is, the secret one hidden, either way round, and
// the note names only the secret file.
func TestGapDiffEachSideOwnPath(t *testing.T) {
	t.Setenv("SC_HOME", filepath.Join(t.TempDir(), "home"))
	secretEnv(t)
	mustSC(t, "init")
	dir := t.TempDir()
	plain, shadow := filepath.Join(dir, "hosts"), filepath.Join(dir, "shadow")
	os.WriteFile(plain, []byte("password=plain1\n"), 0o644)
	os.WriteFile(shadow, []byte("root:$6$s$sekrit1:19000::::::\n"), 0o600)
	idP, idS := snapID(t, plain), snapID(t, shadow)
	for _, args := range [][]string{{"diff", idP, idS}, {"diff", idS, idP}} {
		r := sc(t, args...)
		if r.code != 0 || strings.Contains(r.stdout, "sekrit1") || !strings.Contains(r.stdout, "password=plain1\n") ||
			!strings.Contains(r.stdout, "root:<hidden") || !strings.Contains(r.stderr, "sc: note: the secret values of "+shadow+" are shown as ") ||
			strings.Contains(r.stderr, plain) {
			t.Errorf("sc %v: %+v", args, r)
		}
	}
}

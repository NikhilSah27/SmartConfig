package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withPasswd points sc scope at a passwd file of lines.
func withPasswd(t *testing.T, lines ...string) {
	passwd := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(passwd, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	passwdPath = passwd
	t.Cleanup(func() { passwdPath = "/etc/passwd" })
}

func TestScopeCLI(t *testing.T) {
	t.Setenv("SC_HOME", "/var/lib/smartconfig")
	homes := t.TempDir()
	u := filepath.Join(homes, "u")
	os.MkdirAll(filepath.Join(u, ".ssh"), 0o700)
	// root's home has no .ssh: scd does not watch it, nor list it.
	withPasswd(t, "root:x:0:0::"+filepath.Join(homes, "root")+":/bin/bash", "u:x:1000:1000::"+u+":/bin/bash", "d:x:1:1::/usr/sbin:/usr/sbin/nologin")
	roots := "/etc, " + u + "/.ssh"
	if fi, err := os.Lstat("/boot/grub"); err == nil && fi.IsDir() {
		roots = "/etc, /boot/grub, " + u + "/.ssh"
	}

	r := sc(t, "scope", "/etc/fstab", "/etc/ssl/certs/x.pem", u+"/.ssh/authorized_keys", "/etc/ssh/ssh_host_ed25519_key",
		"/usr/bin/ls", "/var/lib/smartconfig/changes.db", "/etc/ld.so.cache")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	blocks := strings.Split(r.stdout, "\n\n")
	if len(blocks) != 7 {
		t.Fatalf("%d blocks:\n%s", len(blocks), r.stdout)
	}
	for i, want := range [][]string{
		{"/etc/fstab\n  recorded: yes, under /etc\n  tier:     1 (boot), scope line ", "\n            tier 1 /etc/{fstab,",
			"\n  checker:  fstab (sc check /etc/fstab)\n  applies:  at the next boot"},
		{"/etc/ssl/certs/x.pem\n  recorded: no, /etc/ssl/certs is left out by scope line ", "\n            exclude /etc/ssl/certs/**\n  checker:  none"},
		{u + "/.ssh/authorized_keys\n  recorded: yes, under " + u + "/.ssh, kept by scope line ", "\n  tier:     2 (access)"},
		{"  content:  a fingerprint only, never stored or shown, scope line ", "\n            digest /etc/ssh/ssh_host_"},
		{"/usr/bin/ls\n  recorded: no, it is outside every watched directory:\n            " + roots + "\n  checker:  none"},
		{"/var/lib/smartconfig/changes.db\n  recorded: no, it is in sc's own data directory\n  checker:  none"},
		{"/etc/ld.so.cache\n  recorded: no, left out by scope line ", "\n            exclude /etc/ld.so.cache\n  checker:  none"},
	} {
		for _, w := range want {
			if !strings.Contains(blocks[i], w) {
				t.Errorf("block %d lacks %q:\n%s", i, w, blocks[i])
			}
		}
		if strings.Contains(blocks[i], "tier:") != (i == 0 || i == 2 || i == 3) {
			t.Errorf("block %d: a tier line only for recorded paths:\n%s", i, blocks[i])
		}
	}
	// Every line but a scope rule's own text fits 80 columns (a test's
	// home is long).
	for _, l := range strings.Split(strings.ReplaceAll(r.stdout, homes, "/home"), "\n") {
		if len(l) > 80 && !strings.HasPrefix(l, "            ") {
			t.Errorf("wider than 80 columns: %q", l)
		}
	}
	// A relative path is taken from the working directory.
	t.Chdir("/etc")
	if r := sc(t, "scope", "fstab"); r.code != 0 || !strings.HasPrefix(r.stdout, "/etc/fstab\n  recorded: yes") {
		t.Errorf("relative: %+v", r)
	}
	if r := sc(t, "scope"); r.code != 1 || !strings.HasPrefix(r.stderr, "sc: ") {
		t.Errorf("no argument: %+v", r)
	}
}

// Like scd, sc scope looks at each root with lstat: a path under a root
// that is missing, a symlink or not a directory is not recorded, and a
// root sc may not look at leaves the answer to scd, which runs as root.
func TestScopeUnwatchedRoots(t *testing.T) {
	t.Setenv("SC_HOME", "/var/lib/smartconfig")
	homes := t.TempDir()
	home := func(n string) string { os.MkdirAll(filepath.Join(homes, n), 0o755); return filepath.Join(homes, n) }
	link, missing, file, shut := home("link"), home("missing"), home("file"), home("shut")
	os.Mkdir(filepath.Join(link, "real"), 0o700)
	os.Symlink("real", filepath.Join(link, ".ssh"))
	os.WriteFile(filepath.Join(file, ".ssh"), nil, 0o600)
	os.Mkdir(filepath.Join(shut, ".ssh"), 0o700)
	var lines []string
	for _, h := range []string{link, missing, file, shut} {
		lines = append(lines, filepath.Base(h)+":x:1000:1000::"+h+":/bin/bash")
	}
	withPasswd(t, lines...)
	if os.Geteuid() != 0 {
		os.Chmod(shut, 0o000)
		t.Cleanup(func() { os.Chmod(shut, 0o755) })
	}
	r := sc(t, "scope", link+"/.ssh/authorized_keys", missing+"/.ssh/authorized_keys", file+"/.ssh/authorized_keys", shut+"/.ssh/authorized_keys", "/usr/bin/ls")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	blocks := strings.Split(r.stdout, "\n\n")
	if len(blocks) != 5 {
		t.Fatalf("%d blocks:\n%s", len(blocks), r.stdout)
	}
	for i, want := range []string{
		"  recorded: no, scd does not watch " + link + "/.ssh: it is a symlink\n  checker:  none",
		"  recorded: no, scd does not watch " + missing + "/.ssh: it does not exist\n  checker:  none",
		"  recorded: no, scd does not watch " + file + "/.ssh: it is not a directory\n  checker:  none",
	} {
		if !strings.Contains(blocks[i], want) || strings.Contains(blocks[i], "tier:") {
			t.Errorf("block %d lacks %q:\n%s", i, want, blocks[i])
		}
	}
	shutWant := "  recorded: yes, under " + shut + "/.ssh, kept by scope line "
	if os.Geteuid() != 0 {
		shutWant = "  recorded: yes if scd watches " + shut + "/.ssh, which sc cannot look at\n            (permission denied); run sc scope as root to be sure\n  tier:     2 (access)"
	}
	if !strings.Contains(blocks[3], shutWant) {
		t.Errorf("unseen root lacks %q:\n%s", shutWant, blocks[3])
	}
	// The roots scd watches: not the three it skips.
	if strings.Contains(blocks[4], homes+"/link") || strings.Contains(blocks[4], homes+"/missing") ||
		strings.Contains(blocks[4], homes+"/file") || !strings.Contains(blocks[4], shut+"/.ssh") {
		t.Errorf("watched roots:\n%s", blocks[4])
	}
}

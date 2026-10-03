package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopeCLI(t *testing.T) {
	t.Setenv("SC_HOME", "/var/lib/smartconfig")
	passwd := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(passwd, []byte("root:x:0:0::/root:/bin/bash\nu:x:1000:1000::/home/u:/bin/bash\nd:x:1:1::/usr/sbin:/usr/sbin/nologin\n"), 0o644)
	passwdPath = passwd
	t.Cleanup(func() { passwdPath = "/etc/passwd" })

	r := sc(t, "scope", "/etc/fstab", "/etc/ssl/certs/x.pem", "/home/u/.ssh/authorized_keys", "/etc/ssh/ssh_host_ed25519_key",
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
		{"/home/u/.ssh/authorized_keys\n  recorded: yes, under /home/u/.ssh, kept by scope line ", "\n  tier:     2 (access)"},
		{"  content:  a fingerprint only, never stored or shown, scope line ", "\n            digest /etc/ssh/ssh_host_"},
		{"/usr/bin/ls\n  recorded: no, it is outside every watched directory:\n            /etc, /boot/grub, /root/.ssh, /home/u/.ssh\n  checker:  none"},
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
	// Every line but a scope rule's own text fits 80 columns.
	for _, l := range strings.Split(r.stdout, "\n") {
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

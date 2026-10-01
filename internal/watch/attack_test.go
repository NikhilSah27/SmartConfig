//go:build linux

package watch

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// asUser runs a shell script as uid 1000, the attacker of these tests.
func asUser(t *testing.T, script string) {
	t.Helper()
	out, err := exec.Command("setpriv", "--reuid=1000", "--regid=1000", "--clear-groups", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("as uid 1000: %v: %s", err, out)
	}
}

// attackEnv is a root watcher whose login home belongs to uid 1000, and a
// root-only "vault" standing in for /etc/shadow.
func attackEnv(t *testing.T) (*env, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: the watcher runs as root, the attacker as uid 1000")
	}
	if _, err := exec.LookPath("setpriv"); err != nil {
		t.Skip(err)
	}
	e := newEnv(t)
	base := filepath.Dir(e.root)
	for _, d := range []string{filepath.Dir(base), base, filepath.Join(base, "users")} {
		os.Chmod(d, 0o755)
	}
	exec.Command("chown", "-R", "1000:1000", e.user).Run()
	vault := filepath.Join(base, "vault")
	os.MkdirAll(filepath.Join(vault, "etc"), 0o700)
	put(t, filepath.Join(vault, "etc", "shadow"), "ROOT-SECRET\n", 0o600)
	return e, vault
}

func objectsHold(t *testing.T, home, needle string) bool {
	found := false
	filepath.WalkDir(filepath.Join(home, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), needle) {
				found = true
			}
		}
		return nil
	})
	return found
}

// The chunk D review's attack: a directory named authorized_keys in ~/.ssh,
// then a directory in it swapped for a symlink to a root-only directory.
// The scope records nothing below .ssh entries, so nothing is read.
func TestAttackSymlinkBelowSSH(t *testing.T) {
	e, vault := attackEnv(t)
	e.start()
	ak := filepath.Join(e.user, ".ssh", "authorized_keys")
	p := filepath.Join(ak, "a", "etc", "shadow")
	asUser(t, "mkdir -p "+ak+"/a/etc && echo decoy > "+p)
	e.barrier()
	asUser(t, "cd "+ak+" && mv a a.old && ln -s "+vault+" a")
	e.w.requestRescan()
	e.barrier()
	if h := e.history(p); len(h) != 0 {
		t.Errorf("a path below .ssh/authorized_keys/ recorded: %q", h)
	}
	if objectsHold(t, e.home, "ROOT-SECRET") {
		t.Fatal("root-only content stored")
	}
}

// The same swap where the scope does record the tree (a user-writable root
// recorded in full): the read itself refuses to pass the symlink.
func TestAttackSymlinkReadLayer(t *testing.T) {
	e, vault := attackEnv(t)
	exec.Command("chown", "-R", "1000:1000", e.root).Run()
	e.start()
	p := filepath.Join(e.root, "a", "etc", "shadow")
	asUser(t, "mkdir -p "+e.root+"/a/etc && echo decoy > "+p)
	e.waitFor("the decoy", func() bool { return len(e.history(p)) > 0 })
	asUser(t, "cd "+e.root+" && mv a a.old && ln -s "+vault+" a")
	e.w.requestRescan()
	e.barrier()
	for _, h := range e.history(p) {
		if strings.Contains(h, "owner 1000:1000->0:0") {
			t.Errorf("root-owned file read through the symlink: %q", e.history(p))
		}
	}
	if objectsHold(t, e.home, "ROOT-SECRET") {
		t.Fatal("root-only content stored")
	}
	if !strings.Contains(e.log.String(), "a directory on the way is a symlink") {
		t.Errorf("refusal not logged:\n%s", e.log.String())
	}
}

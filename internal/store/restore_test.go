package store

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/fsutil"
)

// storeShape counts rows and object files, to show a refusal wrote nothing.
func storeShape(t *testing.T, s *Store) (rows, objects int) {
	t.Helper()
	cs, err := s.List("", 0)
	if err != nil {
		t.Fatal(err)
	}
	filepath.WalkDir(filepath.Join(s.dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			objects++
		}
		return nil
	})
	return len(cs), objects
}

// refuse checks that Restore(id) fails with one line saying msg and leaves
// the store exactly as it was.
func refuse(t *testing.T, s *Store, id, msg string) {
	t.Helper()
	rows, objects := storeShape(t, s)
	restored, prev, err := s.Restore(id)
	if err == nil || !strings.Contains(err.Error(), msg) || strings.Contains(err.Error(), "\n") {
		t.Fatalf("Restore(%s): %v, want %q", id, err, msg)
	}
	if restored.ID != "" || prev != nil {
		t.Fatalf("Restore(%s) returned %+v, %+v", id, restored, prev)
	}
	if r, o := storeShape(t, s); r != rows || o != objects {
		t.Fatalf("Restore(%s) wrote: rows %d->%d, objects %d->%d", id, rows, r, objects, o)
	}
}

func TestRestoreLink(t *testing.T) {
	s, dir := setup(t)
	tick := clock(s)
	l := filepath.Join(dir, "unit")
	os.Symlink("/dev/null", l)
	masked, _ := snap(t, s, l)
	tick()
	os.Remove(l)
	os.Symlink("/lib/systemd/system/x.service", l)
	snap(t, s, l)
	tick()
	restored, prev, err := s.Restore(masked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(l); got != "/dev/null" {
		t.Fatalf("link -> %q", got)
	}
	if restored.Kind != KindLink || restored.Target != "/dev/null" || restored.Blob != "" || restored.Origin != OriginRestore {
		t.Fatalf("restore row %+v", restored)
	}
	if prev == nil || prev.Kind != KindLink || prev.Target != "/lib/systemd/system/x.service" || prev.Origin != OriginPreRestore {
		t.Fatalf("pre-restore row %+v", prev)
	}
	wantHistory(t, s, l, "link test", "link test", "link before restoring "+masked.ID, "link restored from "+masked.ID)
}

// A file can replace a link and a link a file.
func TestRestoreFileOverLinkAndBack(t *testing.T) {
	s, dir := setup(t)
	tick := clock(s)
	p := filepath.Join(dir, "conf")
	write(t, p, "real\n", 0o600)
	file, _ := snap(t, s, p)
	tick()
	os.Remove(p)
	os.Symlink("/etc/other", p)
	link, _ := snap(t, s, p)
	tick()
	if _, prev, err := s.Restore(file.ID); err != nil || prev == nil || prev.Kind != KindLink {
		t.Fatalf("file over link: %+v %v", prev, err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "real\n" {
		t.Fatalf("content %q %v", b, err)
	}
	if fi, _ := os.Lstat(p); fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	tick()
	if _, prev, err := s.Restore(link.ID); err != nil || prev == nil || prev.Kind != KindFile {
		t.Fatalf("link over file: %+v %v", prev, err)
	}
	if got, err := os.Readlink(p); err != nil || got != "/etc/other" {
		t.Fatalf("link -> %q %v", got, err)
	}
	if got := names(t, dir); got != "conf" {
		t.Fatalf("temp files left: %q", got)
	}
}

func names(t *testing.T, dir string) string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	return strings.Join(n, " ")
}

// Restoring a "did not exist" row removes the file, after a pre-restore
// row; restoring it again is nothing to do.
func TestRestoreDeletion(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "x.service")
	write(t, p, "[Unit]\n", 0o644)
	o := observe(t, p)
	o.Created = true
	record(t, s, o)
	cs, _ := s.List(p, 0)
	gone := cs[1] // did not exist
	if gone.Kind != KindDeleted {
		t.Fatalf("rows %+v", cs)
	}
	restored, prev, err := s.Restore(gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
	if restored.Kind != KindDeleted || restored.ID == "" || prev == nil || prev.Kind != KindFile {
		t.Fatalf("rows %+v %+v", restored, prev)
	}
	wantHistory(t, s, p, "deleted did not exist", "file created", "file before restoring "+gone.ID, "deleted restored from "+gone.ID)

	rows, objects := storeShape(t, s)
	restored, prev, err = s.Restore(gone.ID)
	if err != nil || restored.ID != "" || prev != nil {
		t.Fatalf("already absent: %+v %+v %v", restored, prev, err)
	}
	if r, o := storeShape(t, s); r != rows || o != objects {
		t.Fatal("nothing-to-do wrote something")
	}
	// A deleted row whose parent directory is gone is nothing to do too.
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	q := filepath.Join(sub, "f")
	write(t, q, "x", 0o644)
	record(t, s, observe(t, q))
	os.RemoveAll(sub)
	record(t, s, observe(t, q))
	cs, _ = s.List(q, 1)
	if restored, _, err := s.Restore(cs[0].ID); err != nil || restored.ID != "" {
		t.Fatalf("parent gone: %+v %v", restored, err)
	}
	// Restoring the "created" row recreates the file.
	cs, _ = s.List(p, 0)
	if _, prev, err := s.Restore(cs[2].ID); err != nil || prev != nil {
		t.Fatalf("recreate: %+v %v", prev, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "[Unit]\n" {
		t.Fatalf("content %q", b)
	}
}

// A fingerprint-only path is never restored, whatever the row's kind, and
// a digest row never is.
func TestRestoreRefusesFingerprint(t *testing.T) {
	s, dir := setup(t)
	key := filepath.Join(dir, "ssh_host_rsa_key")
	// Rows an M1 sc would have written: content stored, then a deletion.
	s.SetFingerprintOnly(func(string) bool { return false })
	write(t, key, "PRIVATE\n", 0o600)
	fileRow, _ := snap(t, s, key)
	os.Remove(key)
	record(t, s, observe(t, key))
	cs, _ := s.List(key, 1)
	deletedRow := cs[0]
	write(t, key, "NEW PRIVATE\n", 0o600)

	s.SetFingerprintOnly(func(p string) bool { return p == key })
	for _, id := range []string{fileRow.ID, deletedRow.ID} {
		refuse(t, s, id, key+" is fingerprint-only; sc never restores it")
	}
	if b, _ := os.ReadFile(key); string(b) != "NEW PRIVATE\n" {
		t.Fatal("the key was touched")
	}

	// A digest row (a user file over the size limit).
	u := filepath.Join(dir, "authorized_keys")
	write(t, u, "ssh-ed25519 AAAA\n", 0o600)
	o := observe(t, u)
	o.Digest = true
	digest := record(t, s, o)[0].Change
	write(t, u, "changed\n", 0o600)
	refuse(t, s, digest.ID, digest.ID+" keeps only a fingerprint of "+u+", not its content")
}

// Every directory down to the target's parent must be a real directory
// owned by root or the caller.
func TestRestoreRefusesUnsafeDir(t *testing.T) {
	s, dir := setup(t)
	real := filepath.Join(dir, "real")
	os.Mkdir(real, 0o755)
	p := filepath.Join(real, "conf")
	write(t, p, "x\n", 0o644)
	c, _ := snap(t, s, p)
	write(t, p, "y\n", 0o644)

	// A row whose path goes through a symlink (an M1 row, or a moved dir).
	os.Symlink(real, filepath.Join(dir, "link"))
	viaLink := filepath.Join(dir, "link", "conf")
	db := rawDB(t, Home())
	if _, err := db.Exec(`UPDATE changes SET path = ? WHERE id = ?`, viaLink, c.ID); err != nil {
		t.Fatal(err)
	}
	refuse(t, s, c.ID, "refusing to restore into "+filepath.Join(dir, "link")+" (a symlink); see it with: sc cat "+c.ID)

	// A symlink higher up than the parent (chunk B review).
	sub := filepath.Join(real, "sub")
	os.Mkdir(sub, 0o755)
	write(t, filepath.Join(sub, "conf"), "z\n", 0o644)
	if _, err := db.Exec(`UPDATE changes SET path = ? WHERE id = ?`, filepath.Join(dir, "link", "sub", "conf"), c.ID); err != nil {
		t.Fatal(err)
	}
	refuse(t, s, c.ID, "refusing to restore into "+filepath.Join(dir, "link")+" (a symlink)")

	// A missing directory: refused before anything is written.
	if _, err := db.Exec(`UPDATE changes SET path = ? WHERE id = ?`, filepath.Join(dir, "nodir", "conf"), c.ID); err != nil {
		t.Fatal(err)
	}
	refuse(t, s, c.ID, "restore "+filepath.Join(dir, "nodir", "conf")+": ")
	if got := names(t, dir); strings.Contains(got, "sc-tmp") {
		t.Fatalf("temp file left: %q", got)
	}

	// A directory owned by another user (root half).
	if os.Geteuid() != 0 {
		t.Skip("the owner check needs root")
	}
	if err := os.Chown(real, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE changes SET path = ? WHERE id = ?`, p, c.ID); err != nil {
		t.Fatal(err)
	}
	refuse(t, s, c.ID, "refusing to restore into "+real+" (owner uid 65534, not root); see it with: sc cat "+c.ID)
	if b, _ := os.ReadFile(p); string(b) != "y\n" {
		t.Fatal("the file was touched")
	}
}

// A watcher that records while a restore is in progress writes no auto row
// after the restore row: one that read before the restore finds the path
// moved, one that reads after it finds the restore row equal.
func TestRecordDuringRestore(t *testing.T) {
	for _, kind := range []string{KindFile, KindLink, KindDeleted} {
		t.Run(kind, func(t *testing.T) {
			s, dir := setup(t)
			p := filepath.Join(dir, "conf")
			var target Change
			switch kind {
			case KindFile:
				write(t, p, "good\n", 0o644)
				target, _ = snap(t, s, p)
				write(t, p, "broken\n", 0o644)
			case KindLink:
				os.Symlink("good", p)
				target, _ = snap(t, s, p)
				os.Remove(p)
				os.Symlink("broken", p)
			case KindDeleted:
				write(t, p, "x\n", 0o644)
				o := observe(t, p)
				o.Created = true
				record(t, s, o)
				cs, _ := s.List(p, 0)
				target = cs[1]
			}
			before := observe(t, p) // read before the restore
			type result struct {
				res []Result
				err error
			}
			done := make(chan result, 1)
			testHookAfterRestoreWrite = func() {
				after := observe(t, p) // read after the write, before the commit
				go func() {
					res, err := s.Record([]Obs{after})
					done <- result{res, err}
				}()
				time.Sleep(50 * time.Millisecond) // let it reach BEGIN IMMEDIATE
			}
			defer func() { testHookAfterRestoreWrite = nil }()
			restored, _, err := s.Restore(target.ID)
			if err != nil || restored.ID == "" {
				t.Fatalf("restore: %+v %v", restored, err)
			}
			if r := <-done; r.err != nil || r.res[0].Recorded || r.res[0].Moved {
				t.Fatalf("read after the write: %+v %v", r.res, r.err)
			}
			if r := record(t, s, before); r[0].Recorded || !r[0].Moved {
				t.Fatalf("read before the restore: %+v", r[0])
			}
			cs, _ := s.List(p, 1)
			if cs[0].Origin != OriginRestore {
				t.Fatalf("newest row %+v", cs[0])
			}
		})
	}
}

// A link or deletion restore whose path changes after the pre-restore
// read starts over, so the change is saved before the restore replaces it
// (chunk B review).
func TestRestoreLinkAndDeletionRecheckStamp(t *testing.T) {
	for _, kind := range []string{KindLink, KindDeleted} {
		t.Run(kind, func(t *testing.T) {
			s, dir := setup(t)
			p := filepath.Join(dir, "conf")
			var src Change
			if kind == KindLink {
				os.Symlink("good", p)
				src, _ = snap(t, s, p)
				os.Remove(p)
				os.Symlink("broken", p)
			} else {
				write(t, p, "x\n", 0o644)
				o := observe(t, p)
				o.Created = true
				record(t, s, o)
				cs, _ := s.List(p, 0)
				src = cs[1]
			}
			edits := 0
			testHookBeforeRestoreLock = func() {
				if edits == 0 { // a late edit, once
					os.Remove(p)
					write(t, p, "late edit\n", 0o644)
				}
				edits++
			}
			defer func() { testHookBeforeRestoreLock = nil }()
			if _, prev, err := s.Restore(src.ID); err != nil || prev == nil || prev.Kind != KindFile || prev.Size != 10 {
				t.Fatalf("restore: %+v %v", prev, err)
			}
			if edits != 2 {
				t.Fatalf("%d attempts, want 2", edits)
			}
		})
	}
}

// A path removed after the refusals and before the pre-restore read
// leaves a deletion restore with nothing to do and no row.
func TestRestoreDeletionGoneMeanwhile(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "x\n", 0o644)
	o := observe(t, p)
	o.Created = true
	record(t, s, o)
	cs, _ := s.List(p, 0)
	rows, objects := storeShape(t, s)
	testHookBeforePreRestore = func() { os.Remove(p) }
	defer func() { testHookBeforePreRestore = nil }()
	restored, prev, err := s.Restore(cs[1].ID)
	if err != nil || restored.ID != "" || prev != nil {
		t.Fatalf("%+v %+v %v", restored, prev, err)
	}
	if r, ob := storeShape(t, s); r != rows || ob != objects {
		t.Fatal("rows or objects added")
	}
}

// A restored link gets the recorded owner (root half).
func TestRestoreLinkOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	s, dir := setup(t)
	l := filepath.Join(dir, "unit")
	os.Symlink("/nonexistent", l)
	if err := os.Lchown(l, 1234, 5678); err != nil {
		t.Fatal(err)
	}
	c, _ := snap(t, s, l)
	os.Remove(l)
	os.Symlink("/other", l)
	if _, _, err := s.Restore(c.ID); err != nil {
		t.Fatal(err)
	}
	st, err := fsutil.ReadState(l)
	if err != nil || st.Meta.UID != 1234 || st.Meta.GID != 5678 || st.Target != "/nonexistent" {
		t.Fatalf("%+v %v", st, err)
	}
}

// An immutable (+i) or append-only (+a) target or directory is refused
// before anything is written, with the chattr to run, every flag at once
// (M2's deferred "+i/+a precheck"); any other refusal comes first, and an
// absent file's deletion needs nothing. Setting the flags needs root.
func TestRestoreRefusesImmutable(t *testing.T) {
	chattr, err := exec.LookPath("chattr")
	if os.Geteuid() != 0 || err != nil {
		t.Skip("needs root and chattr")
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	f := filepath.Join(dir, "conf")
	os.WriteFile(f, []byte("good\n"), 0o644)
	c, _, err := s.Snapshot(f, OriginManual, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f, []byte("bad\n"), 0o644)
	for _, tc := range []struct{ flag, on, want string }{
		{"+i", f, f + " is immutable (chattr +i), so " + f + " cannot be replaced: run chattr -i " + f + " first"},
		{"+a", f, f + " is append-only (chattr +a), so " + f + " cannot be replaced: run chattr -a " + f + " first"},
		{"+ia", f, f + " is immutable and append-only (chattr +ia), so " + f + " cannot be replaced: run chattr -ia " + f + " first"},
		{"+i", dir, dir + " is immutable (chattr +i), so " + f + " cannot be replaced: run chattr -i " + dir + " first"},
		{"+a", dir, dir + " is append-only (chattr +a), so " + f + " cannot be replaced: run chattr -a " + dir + " first"},
	} {
		if out, err := exec.Command(chattr, tc.flag, tc.on).CombinedOutput(); err != nil {
			t.Skipf("chattr %s: %v %s (a filesystem without the flag)", tc.flag, err, out)
		}
		_, _, err := s.Restore(c.ID)
		off := "-" + tc.flag[1:]
		exec.Command(chattr, off, tc.on).Run()
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasSuffix(err.Error(), "(file not changed)") {
			t.Errorf("%s: %v", tc.flag, err)
		}
		if b, _ := os.ReadFile(f); string(b) != "bad\n" {
			t.Errorf("%s: the file changed: %q", tc.flag, b)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".*sc-tmp*")); len(left) != 0 {
			t.Errorf("%s: temp files left: %v", tc.flag, left)
		}
	}
	// sc edit asks before the editor opens.
	exec.Command(chattr, "+i", f).Run()
	err = s.CanReplace(f, nil)
	exec.Command(chattr, "-i", f).Run()
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("can replace: %v", err)
	}
	// Without the flags, a restore goes through.
	if _, _, err := s.Restore(c.ID); err != nil {
		t.Errorf("restore: %v", err)
	}
	// Both at once: one message, both chattr commands.
	exec.Command(chattr, "+i", f).Run()
	exec.Command(chattr, "+a", dir).Run()
	_, _, err = s.Restore(c.ID)
	exec.Command(chattr, "-i", f).Run()
	exec.Command(chattr, "-a", dir).Run()
	if err == nil || !strings.Contains(err.Error(), "run chattr -i "+f+" and chattr -a "+dir+" first") {
		t.Errorf("both: %v", err)
	}
	// A digest row's own refusal comes before the chattr advice.
	s.SetFingerprintOnly(func(p string) bool { return p == f })
	os.WriteFile(f, []byte("secret\n"), 0o600)
	st, err := fsutil.ReadState(f)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Record([]Obs{{Path: f, State: &st, Origin: OriginAuto}})
	s.SetFingerprintOnly(func(string) bool { return false })
	if err != nil || d[0].Change.Kind != KindDigest {
		t.Fatalf("digest row: %+v %v", d, err)
	}
	exec.Command(chattr, "+i", f).Run()
	_, _, err = s.Restore(d[0].Change.ID)
	exec.Command(chattr, "-i", f).Run()
	if err == nil || !strings.Contains(err.Error(), "keeps only a fingerprint") {
		t.Errorf("digest before chattr: %v", err)
	}
	// A deletion row whose file is already gone needs nothing written:
	// no refusal, even in an immutable directory.
	os.Remove(f)
	gone, err := s.Record([]Obs{{Path: f, Origin: OriginAuto}})
	if err != nil || len(gone) != 1 || gone[0].Change.Kind != KindDeleted {
		t.Fatalf("deletion row: %+v %v", gone, err)
	}
	exec.Command(chattr, "+i", dir).Run()
	_, _, err = s.Restore(gone[0].Change.ID)
	exec.Command(chattr, "-i", dir).Run()
	if err != nil {
		t.Errorf("an absent file's deletion: %v", err)
	}
}

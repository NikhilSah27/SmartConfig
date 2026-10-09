package store

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"smartconfig/internal/fsutil"
)

// pathState is what a refused restore must not change about a path: the
// content's sha256 or the link text, mode, owner and inode.
func pathState(t *testing.T, p string) string {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	what, _ := os.Readlink(p)
	if fi.Mode()&os.ModeSymlink == 0 {
		b, _ := os.ReadFile(p)
		what = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	return fmt.Sprintf("%s %v %d:%d %d", what, fi.Mode(), st.Uid, st.Gid, st.Ino)
}

// TestM1Compat runs the M1 sc ($SC_M1_BIN, see make m1-compat) on a store
// the M2 code migrated and filled with every row kind.
func TestM1Compat(t *testing.T) {
	bin := os.Getenv("SC_M1_BIN")
	if bin == "" {
		t.Skip("SC_M1_BIN is not set; run: make m1-compat")
	}
	s, dir := setup(t)
	tick := clock(s)
	m1 := func(args ...string) (int, string, string) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "SC_HOME="+Home())
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, out.String(), errb.String()
	}

	// Every kind, written by the M2 code.
	file := filepath.Join(dir, "hosts")
	write(t, file, "127.0.0.1 localhost\n", 0o644)
	fileRow, _ := snap(t, s, file)
	tick()
	link := filepath.Join(dir, "localtime")
	os.Symlink("/usr/share/zoneinfo/Etc/UTC", link)
	linkRow, _ := snap(t, s, link)
	os.Remove(link)
	os.Symlink("/usr/share/zoneinfo/Asia/Kolkata", link)
	gone := filepath.Join(dir, "gone.conf")
	write(t, gone, "x\n", 0o644)
	record(t, s, observe(t, gone))
	os.Remove(gone)
	deletedRow := record(t, s, observe(t, gone))[0].Change
	write(t, gone, "back\n", 0o600)
	fresh := filepath.Join(dir, "x.service")
	write(t, fresh, "[Unit]\n", 0o644)
	o := observe(t, fresh)
	o.Created = true
	record(t, s, o)
	cs, _ := s.List(fresh, 0)
	didNotExist := cs[1]
	key := filepath.Join(dir, "big")
	write(t, key, "user data\n", 0o600)
	o = observe(t, key)
	o.Digest = true
	digestRow := record(t, s, o)[0].Change
	write(t, key, "other data\n", 0o600)
	if didNotExist.Kind != KindDeleted || digestRow.Kind != KindDigest || linkRow.Kind != KindLink {
		t.Fatalf("setup rows: %+v %+v %+v", didNotExist, digestRow, linkRow)
	}
	total, _ := storeShape(t, s)

	// sc-m1 log lists them all.
	code, out, errs := m1("log", "-n", "0")
	if code != 0 || len(strings.Split(strings.TrimSpace(out), "\n")) != total+1 {
		t.Fatalf("sc-m1 log: %d %q %q", code, out, errs)
	}
	// cat, diff and restore of the file row work.
	if code, out, _ := m1("cat", fileRow.ID); code != 0 || out != "127.0.0.1 localhost\n" {
		t.Fatalf("sc-m1 cat: %d %q", code, out)
	}
	write(t, file, "broken\n", 0o644)
	if code, out, _ := m1("diff", fileRow.ID); code != 0 || !strings.Contains(out, "+broken") {
		t.Fatalf("sc-m1 diff: %d %q", code, out)
	}
	if code, _, errs := m1("restore", fileRow.ID); code != 0 {
		t.Fatalf("sc-m1 restore: %d %q", code, errs)
	}
	if b, _ := os.ReadFile(file); string(b) != "127.0.0.1 localhost\n" {
		t.Fatalf("restored %q", b)
	}
	// A row written by sc edit (M3) is a file row to sc-m1: it lists the
	// origin as it is and restores the row.
	edited := filepath.Join(dir, "edited.conf")
	write(t, edited, "before\n", 0o644)
	st, err := fsutil.ReadState(edited)
	if err != nil {
		t.Fatal(err)
	}
	editRow, _, err := s.Replace(edited, []byte("after\n"), 0o644, os.Getuid(), os.Getgid(), &st, OriginEdit, "sc edit")
	if err != nil {
		t.Fatal(err)
	}
	write(t, edited, "broken\n", 0o644)
	if code, out, _ := m1("log", edited); code != 0 || !strings.Contains(out, editRow.ID+"  ") || !strings.Contains(out, "  edit  ") {
		t.Fatalf("sc-m1 log of an edit row: %d %q", code, out)
	}
	if code, _, errs := m1("restore", editRow.ID); code != 0 {
		t.Fatalf("sc-m1 restore of an edit row: %d %q", code, errs)
	}
	if b, _ := os.ReadFile(edited); string(b) != "after\n" {
		t.Fatalf("sc-m1 restored %q", b)
	}
	total, _ = storeShape(t, s)

	// Every other kind: one line, exit 1, nothing changed, no row.
	for _, c := range []struct {
		row  Change
		path string
	}{{linkRow, link}, {deletedRow, gone}, {didNotExist, fresh}, {digestRow, key}} {
		before := pathState(t, c.path)
		rows, _ := storeShape(t, s)
		code, out, errs := m1("restore", c.row.ID)
		if code != 1 || out != "" || !strings.HasPrefix(errs, "sc: ") || strings.Count(errs, "\n") != 1 {
			t.Errorf("sc-m1 restore %s row: %d %q %q", c.row.Kind, code, out, errs)
		}
		if after := pathState(t, c.path); after != before {
			t.Errorf("sc-m1 restore %s row changed %s", c.row.Kind, c.path)
		}
		if n, _ := storeShape(t, s); n != rows {
			t.Errorf("sc-m1 restore %s row added %d rows", c.row.Kind, n-rows)
		}
	}

	// sc-m1 snapshot adds a file row, and the M2 code reads what it wrote.
	write(t, file, "127.0.0.1 localhost\n::1 localhost\n", 0o644)
	code, out, errs = m1("snapshot", "-q", file)
	if code != 0 {
		t.Fatalf("sc-m1 snapshot: %q", errs)
	}
	c, err := s.Get(strings.TrimSpace(out))
	if err != nil || c.Kind != KindFile || c.Target != "" || c.Size != int64(len("127.0.0.1 localhost\n::1 localhost\n")) {
		t.Fatalf("row by sc-m1: %+v %v", c, err)
	}
	if res := record(t, s, observe(t, file)); res[0].Recorded {
		t.Fatal("the M2 code does not see sc-m1's row as the newest state")
	}

	// After sc prune (M5 follow-up 2), sc-m1 still lists and prints what
	// is left, and its new rows come after every old one.
	ageBlobs(t, s)
	if got, err := s.Prune(time.Unix(1_800_000_000, 0), nil, false); err != nil || got.Rows == 0 {
		t.Fatalf("prune: %+v %v", got, err)
	}
	if code, out, errs = m1("log", file); code != 0 || !strings.Contains(out, c.ID) {
		t.Fatalf("sc-m1 log after prune: %d %q %q", code, out, errs)
	}
	if code, out, _ = m1("cat", c.ID); code != 0 || out != "127.0.0.1 localhost\n::1 localhost\n" {
		t.Fatalf("sc-m1 cat after prune: %d %q", code, out)
	}
	before, _ := s.NewestRowID()
	write(t, file, "after prune\n", 0o644)
	if code, out, errs = m1("snapshot", "-q", file); code != 0 {
		t.Fatalf("sc-m1 snapshot after prune: %q", errs)
	}
	if after, _ := s.NewestRowID(); after <= before {
		t.Fatalf("sc-m1's row has rowid %d, after %d", after, before)
	}
	var ok string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check %q %v", ok, err)
	}
}

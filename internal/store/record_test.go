package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/fsutil"
)

// observe reads p as the watcher would: its state, or nil if absent.
func observe(t *testing.T, p string) Obs {
	t.Helper()
	o := Obs{Path: p, CheckStamp: true, Origin: OriginAuto}
	st, err := fsutil.ReadState(p)
	switch {
	case fsutil.IsNotExist(err):
	case err != nil:
		t.Fatal(err)
	default:
		o.State = &st
	}
	return o
}

func record(t *testing.T, s *Store, obs ...Obs) []Result {
	t.Helper()
	res, err := s.Record(obs)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// history returns "kind intent" of p's rows, oldest first.
func history(t *testing.T, s *Store, p string) []string {
	t.Helper()
	cs, err := s.List(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(cs) - 1; i >= 0; i-- {
		out = append(out, cs[i].Kind+" "+cs[i].Intent)
	}
	return out
}

func wantHistory(t *testing.T, s *Store, p string, want ...string) {
	t.Helper()
	if got := history(t, s, p); strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("%s history:\n got %q\nwant %q", p, got, want)
	}
}

func TestRecordFileChanges(t *testing.T) {
	s, dir := setup(t)
	tick := clock(s)
	p := filepath.Join(dir, "conf")
	write(t, p, "a\n", 0o644)
	record(t, s, observe(t, p))
	first, _ := s.List(p, 1)

	// touch changes only the times: no row.
	later := time.Now().Add(time.Hour)
	os.Chtimes(p, later, later)
	if r := record(t, s, observe(t, p)); r[0].Recorded {
		t.Fatal("touch recorded")
	}
	tick()
	os.Chmod(p, 0o600)
	r := record(t, s, observe(t, p))
	if !r[0].Recorded || r[0].Change.Blob != first[0].Blob {
		t.Fatalf("chmod: %+v", r[0])
	}
	tick()
	write(t, p, "b\n", 0o640)
	record(t, s, observe(t, p))
	wantHistory(t, s, p, "file first seen", "file mode 0644->0600", "file changed, mode 0600->0640")
}

func TestRecordOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "a\n", 0o644)
	record(t, s, observe(t, p))
	os.Chown(p, 1000, 1000)
	record(t, s, observe(t, p))
	wantHistory(t, s, p, "file first seen", "file owner 0:0->1000:1000")
}

func TestRecordLinks(t *testing.T) {
	s, dir := setup(t)
	l := filepath.Join(dir, "unit")
	os.Symlink("/dev/null", l)
	record(t, s, observe(t, l))
	os.Remove(l)
	os.Symlink("/lib/systemd/system/x.service", l)
	record(t, s, observe(t, l))
	// A link becomes a file, and a link again.
	os.Remove(l)
	write(t, l, "[Unit]\n", 0o644)
	record(t, s, observe(t, l))
	os.Remove(l)
	os.Symlink("/dev/null", l)
	record(t, s, observe(t, l))
	wantHistory(t, s, l, "link first seen", "link link -> /lib/systemd/system/x.service",
		"file now a file", "link now a link -> /dev/null")
	cs, _ := s.List(l, 1)
	if c := cs[0]; c.Blob != "" || c.Target != "/dev/null" || c.Size != int64(len("/dev/null")) || c.Mode&os.ModeSymlink == 0 {
		t.Fatalf("link row %+v", c)
	}
}

func TestRecordDeleteAndRecreate(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "a\n", 0o644)
	record(t, s, observe(t, p))
	os.Remove(p)
	record(t, s, observe(t, p))
	if r := record(t, s, observe(t, p)); r[0].Recorded {
		t.Fatal("second deletion recorded")
	}
	write(t, p, "a\n", 0o644)
	record(t, s, observe(t, p))
	wantHistory(t, s, p, "file first seen", "deleted deleted", "file created")
	cs, _ := s.List(p, 2)
	if d := cs[1]; d.Blob != "" || d.Size != 0 || d.Mode != 0 || d.Target != "" {
		t.Fatalf("deleted row %+v", d)
	}
	// An absent path that was never recorded gives no row.
	if r := record(t, s, observe(t, filepath.Join(dir, "never"))); r[0].Recorded {
		t.Fatal("absent path recorded")
	}
}

// Created on a new path: "did not exist" and "created" in one transaction,
// with adjacent rowids. On a path that has rows: no "did not exist".
func TestRecordCreated(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "new")
	write(t, p, "a\n", 0o644)
	o := observe(t, p)
	o.Created = true
	record(t, s, o)
	wantHistory(t, s, p, "deleted did not exist", "file created")
	db := rawDB(t, Home())
	var r1, r2, ts1, ts2 int64
	rows, _ := db.Query(`SELECT rowid, ts FROM changes WHERE path = ? ORDER BY rowid`, p)
	rows.Next()
	rows.Scan(&r1, &ts1)
	rows.Next()
	rows.Scan(&r2, &ts2)
	rows.Close()
	if r2 != r1+1 || ts1 != ts2 {
		t.Fatalf("rowids %d %d, ts %d %d", r1, r2, ts1, ts2)
	}

	q := filepath.Join(dir, "old")
	write(t, q, "a\n", 0o644)
	record(t, s, observe(t, q))
	write(t, q, "b\n", 0o644)
	o = observe(t, q)
	o.Created = true
	record(t, s, o)
	wantHistory(t, s, q, "file first seen", "file changed")
}

func TestRecordDigest(t *testing.T) {
	s, dir := setup(t)
	key := filepath.Join(dir, "ssh_host_ed25519_key")
	write(t, key, "PRIVATE KEY\n", 0o600)
	s.SetFingerprintOnly(func(p string) bool { return p == key })
	record(t, s, observe(t, key))
	sum := sha256.Sum256([]byte("PRIVATE KEY\n"))
	cs, _ := s.List(key, 1)
	if c := cs[0]; c.Kind != KindDigest || c.Blob != hex.EncodeToString(sum[:]) || c.Size != 12 || c.Mode != 0o600 {
		t.Fatalf("digest row %+v", c)
	}
	if _, err := os.Stat(s.blobPath(cs[0].Blob)); !os.IsNotExist(err) {
		t.Fatalf("digest content stored: %v", err)
	}
	// A manual snapshot of it is a digest row too, with no object.
	write(t, key, "NEW KEY\n", 0o600)
	c, _, err := s.Snapshot(key, OriginManual, "")
	if err != nil || c.Kind != KindDigest {
		t.Fatalf("manual snapshot: %+v %v", c, err)
	}
	if _, err := os.Stat(s.blobPath(c.Blob)); !os.IsNotExist(err) {
		t.Fatal("manual snapshot stored the content")
	}
	// Obs.Digest works whatever the rule says.
	big := filepath.Join(dir, "big")
	write(t, big, "user data\n", 0o644)
	o := observe(t, big)
	o.Digest = true
	if r := record(t, s, o); r[0].Change.Kind != KindDigest {
		t.Fatalf("Obs.Digest: %+v", r[0])
	}
}

// A path that changed after it was read gives Moved and no row; the rest
// of the batch is recorded.
func TestRecordMoved(t *testing.T) {
	s, dir := setup(t)
	var obs []Obs
	for i := 0; i < MaxBatch; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%02d", i))
		write(t, p, "x\n", 0o644)
		obs = append(obs, observe(t, p))
	}
	moved := obs[17].Path
	write(t, moved+".new", "y\n", 0o644)
	os.Rename(moved+".new", moved)
	n := 0
	for i, r := range record(t, s, obs...) {
		switch {
		case i == 17 && (!r.Moved || r.Recorded):
			t.Fatalf("moved path: %+v", r)
		case i != 17 && (!r.Recorded || r.Moved):
			t.Fatalf("%s: %+v", obs[i].Path, r)
		case r.Recorded:
			n++
		}
	}
	if n != MaxBatch-1 || len(history(t, s, moved)) != 0 {
		t.Fatalf("%d recorded, moved path history %q", n, history(t, s, moved))
	}
	// A path read as absent that exists by the lock is moved too.
	gone := filepath.Join(dir, "gone")
	o := observe(t, gone)
	write(t, gone, "x\n", 0o644)
	record(t, s, observe(t, gone))
	if r := record(t, s, o); !r[0].Moved {
		t.Fatalf("absent then present: %+v", r[0])
	}
	if _, err := s.Record(make([]Obs, MaxBatch+1)); err == nil {
		t.Fatal("51 observations accepted")
	}
}

// Manual snapshots keep M1's "unchanged"; other origins always insert, and
// a file row's id is M1's.
func TestSnapshotOrigins(t *testing.T) {
	s, dir := setup(t)
	tick := clock(s)
	p := filepath.Join(dir, "conf")
	write(t, p, "a\n", 0o644)
	c, unchanged := snap(t, s, p)
	sum := sha256.Sum256([]byte(p + "\n" + strconv.FormatInt(c.TS, 10) + "\n" + c.Blob))
	if unchanged || c.ID != hex.EncodeToString(sum[:])[:6] || c.Kind != KindFile {
		t.Fatalf("first: %+v", c)
	}
	if _, unchanged := snap(t, s, p); !unchanged {
		t.Fatal("equal manual snapshot inserted a row")
	}
	for i := 0; i < 2; i++ {
		tick()
		if _, unchanged, err := s.Snapshot(p, OriginPreRestore, "x"); err != nil || unchanged {
			t.Fatalf("pre-restore %d: %v %v", i, unchanged, err)
		}
	}
	if n := len(history(t, s, p)); n != 3 {
		t.Fatalf("%d rows", n)
	}
	// Ids of link and deleted rows.
	l := filepath.Join(dir, "l")
	os.Symlink("t", l)
	lc, _ := snap(t, s, l)
	sum = sha256.Sum256([]byte(l + "\n" + strconv.FormatInt(lc.TS, 10) + "\nlink\nt"))
	if lc.ID != hex.EncodeToString(sum[:])[:6] {
		t.Fatalf("link id %s", lc.ID)
	}
}

func TestLivePaths(t *testing.T) {
	s, dir := setup(t)
	a, b, l := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "l")
	write(t, a, "x", 0o644)
	write(t, b, "x", 0o644)
	os.Symlink("a", l)
	record(t, s, observe(t, a), observe(t, b), observe(t, l))
	os.Remove(b)
	record(t, s, observe(t, b))
	got, err := s.LivePaths()
	if err != nil || strings.Join(got, " ") != a+" "+l {
		t.Fatalf("%q %v", got, err)
	}
}

// A writer killed inside Record's transaction, after SQLite spilled pages
// into the database file, leaves only committed rows: the next open rolls
// the hot journal back.
func TestCrashMidRecord(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	cmd := helper("record-crash", home)
	cmd.Env = append(cmd.Env, "SC_TEST_DIR="+t.TempDir())
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	lines := bufio.NewScanner(out)
	var committedSize int64
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		if len(f) == 2 && f[0] == "committed" {
			committedSize, _ = strconv.ParseInt(f[1], 10, 64)
		}
		if lines.Text() == "in tx" {
			break
		}
	}
	db := filepath.Join(home, "changes.db")
	fi, err := os.Stat(db)
	if err != nil || committedSize == 0 || fi.Size() <= committedSize {
		t.Fatalf("no cache spill: committed %d, now %v %v", committedSize, fi.Size(), err)
	}
	if _, err := os.Stat(db + "-journal"); err != nil {
		t.Fatalf("no hot journal: %v", err)
	}
	cmd.Process.Kill()
	cmd.Wait()

	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cs, err := s.List("", 0)
	if err != nil || len(cs) != 5 {
		t.Fatalf("%d rows after the crash, want the 5 committed: %v", len(cs), err)
	}
	var ok string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check %q %v", ok, err)
	}
	if _, err := os.Stat(db + "-journal"); !os.IsNotExist(err) {
		t.Fatalf("journal left: %v", err)
	}
}

// recordCrashHelper is the process TestCrashMidRecord kills: it commits 5
// rows, prints the database size, then fills one transaction past a tiny
// page cache (so pages spill into the file) and waits to be killed.
func recordCrashHelper() {
	home, dir := Home(), os.Getenv("SC_TEST_DIR")
	s, err := Open(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.Exec("PRAGMA cache_size = 10"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	obs := func(prefix string, n int, intent string) []Obs {
		var out []Obs
		for i := 0; i < n; i++ {
			p := filepath.Join(dir, fmt.Sprintf("%s%02d", prefix, i))
			os.WriteFile(p, []byte(p), 0o644)
			st, err := fsutil.ReadState(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			out = append(out, Obs{Path: p, State: &st, Origin: OriginAuto, Intent: intent})
		}
		return out
	}
	if _, err := s.Record(obs("c", 5, "")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fi, _ := os.Stat(filepath.Join(home, "changes.db"))
	fmt.Printf("committed %d\n", fi.Size())
	testHookRecordInTx = func() {
		fmt.Println("in tx")
		time.Sleep(time.Hour)
	}
	s.Record(obs("u", MaxBatch, strings.Repeat("x", 20000)))
	os.Exit(1)
}

// The newest row is read again under the write lock: a writer that
// recorded the same state meanwhile leaves nothing for this one to add.
func TestRecordRechecksUnderLock(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "conf")
	write(t, p, "a\n", 0o644)
	o := observe(t, p)
	testHookSnapshotBeforeLock = func() {
		testHookSnapshotBeforeLock = nil
		record(t, s, observe(t, p))
	}
	defer func() { testHookSnapshotBeforeLock = nil }()
	if r := record(t, s, o); r[0].Recorded {
		t.Fatal("recorded twice")
	}
	wantHistory(t, s, p, "file first seen")
}

// The fingerprint rule also holds for a path through a symlinked directory
// (chunk B review D1): its content is never stored.
func TestRecordDigestThroughSymlinkedDir(t *testing.T) {
	s, dir := setup(t)
	real := filepath.Join(dir, "etc")
	os.Mkdir(real, 0o755)
	key := filepath.Join(real, "machine-id")
	write(t, key, "289a1e\n", 0o444)
	s.SetFingerprintOnly(func(p string) bool { return p == key })
	os.Symlink(dir, filepath.Join(dir, "rootlink"))
	for _, p := range []string{
		filepath.Join(dir, "rootlink", "etc", "machine-id"),
		"/proc/self/root" + key,
	} {
		c, _, err := s.Snapshot(p, OriginManual, "")
		if err != nil || c.Kind != KindDigest {
			t.Fatalf("%s: %+v %v", p, c, err)
		}
		if _, err := os.Stat(s.blobPath(c.Blob)); !os.IsNotExist(err) {
			t.Fatalf("%s: content stored", p)
		}
	}
}

// A path that becomes fingerprint-only (an M1 row of a host key) says so,
// and "changed" only when the content changed (chunk B review).
func TestRecordKindChangeIntent(t *testing.T) {
	s, dir := setup(t)
	p := filepath.Join(dir, "machine-id")
	write(t, p, "a\n", 0o644)
	s.SetFingerprintOnly(func(string) bool { return false })
	record(t, s, observe(t, p))
	s.SetFingerprintOnly(func(q string) bool { return q == p })
	record(t, s, observe(t, p))
	write(t, p, "b\n", 0o644)
	record(t, s, observe(t, p))
	s.SetFingerprintOnly(func(string) bool { return false })
	record(t, s, observe(t, p))
	wantHistory(t, s, p, "file first seen", "digest now fingerprint only", "digest changed", "file now with content")
}

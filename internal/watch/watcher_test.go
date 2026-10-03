//go:build linux

package watch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// testScope is a scope over root with the default scope's noise rules, a
// consumer directory (cons) whose names are all recorded except dpkg's
// staging names, and a fingerprint-only file.
func testScope(root string) string {
	return strings.NewReplacer("ROOT", root).Replace(`
root ROOT
exclude **/.*.sc-tmp-*
exclude **/.*.sw[a-p]
exclude **/.*.{swx,swpx}
include **/.ssh/{authorized_keys,authorized_keys2,rc,environment}
exclude **/.ssh/*
exclude **/.ssh/*/*
exclude ROOT/cons/**/*.dpkg-{new,tmp,old,dist,bak,remove,backup}
include ROOT/cons/**
exclude **/*~
exclude **/*.dpkg-{new,tmp,old,dist,bak,remove,backup}
exclude **/{sed??????,XX??????,*.tmp}
exclude **/*.{old,orig,bak,save,rej}
digest ROOT/secret
tier 1 ROOT/fstab
`)
}

// syncBuf is a log that the watcher writes and the test reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// env is one watcher under test.
type env struct {
	t      *testing.T
	root   string // the scope's root
	home   string // SC_HOME
	user   string // a login home from the fixture passwd
	cfg    Config
	w      *Watcher
	log    *syncBuf
	cancel func()
	done   chan error
	st     *store.Store // a second connection, for reading rows
	n      int          // barrier counter
}

// newEnv makes the directories and config; start runs the watcher.
func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{t: t, root: filepath.Join(base, "root"), home: filepath.Join(base, "schome"),
		user: filepath.Join(base, "users", "u"), log: &syncBuf{}}
	os.MkdirAll(e.root, 0o755)
	os.MkdirAll(filepath.Join(e.user, ".ssh"), 0o700)
	passwd := filepath.Join(base, "passwd")
	os.WriteFile(passwd, []byte("u:x:1000:1000::"+e.user+":/bin/bash\nnobody:x:65534:65534::/nonexistent:/usr/sbin/nologin\n"), 0o644)
	e.cfg = Config{
		Home: e.home, ScopeText: testScope(e.root), PasswdPath: passwd,
		Quiet: 50 * time.Millisecond, Cap: time.Second, RescanEvery: time.Hour,
		RescanMinGap: 50 * time.Millisecond, StoreBackoff: 200 * time.Millisecond,
		FloorBackoff: 200 * time.Millisecond, UserFileGap: 100 * time.Millisecond,
		PathBurst: 1000, // tests change one file many times; TestPathRateLimit sets its own
		Log:       e.log,
	}
	return e
}

func (e *env) start() {
	e.t.Helper()
	w, err := New(e.cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.w = w
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan error, 1)
	go func() { e.done <- w.Run(ctx) }()
	if e.st == nil {
		st, err := store.Open(e.home)
		if err != nil {
			e.t.Fatal(err)
		}
		e.st = st
		e.t.Cleanup(func() { st.Close() })
	}
	e.t.Cleanup(e.stop)
	// A large baseline under the race detector can take a while; this is
	// only an upper bound.
	e.waitForWithin(60*time.Second, "the baseline", func() bool { return strings.Contains(e.log.String(), "baseline: ") })
}

// stop cancels the watcher and checks that Run returned nil.
func (e *env) stop() {
	if e.cancel == nil {
		return
	}
	e.cancel()
	e.cancel = nil
	select {
	case err := <-e.done:
		if err != nil {
			e.t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		e.t.Fatal("Run did not return after cancel")
	}
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	e.waitForWithin(5*time.Second, what, cond)
}

func (e *env) waitForWithin(d time.Duration, what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s; log:\n%s", what, e.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// history returns "kind intent" for p's rows, oldest first.
func (e *env) history(p string) []string {
	e.t.Helper()
	cs, err := e.st.List(p, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for i := len(cs) - 1; i >= 0; i-- {
		out = append(out, cs[i].Kind+" "+cs[i].Intent)
	}
	return out
}

func (e *env) newest(p string) store.Change {
	e.t.Helper()
	cs, err := e.st.List(p, 1)
	if err != nil || len(cs) == 0 {
		e.t.Fatalf("no row for %s: %v", p, err)
	}
	return cs[0]
}

// barrier writes a new file and waits for its row: everything marked
// before it has been handled by then (plan 11.2). It does so twice: a
// directory moved away is expanded into its stored paths in the batch
// that may hold the first barrier, and those paths are handled before
// the second. A path put back after a moved stamp can still slip past;
// tests use testHookProcessed or waitFor for that.
func (e *env) barrier() {
	e.t.Helper()
	for i := 0; i < 2; i++ {
		e.n++
		p := filepath.Join(e.root, fmt.Sprintf("barrier-%d", e.n))
		os.WriteFile(p, []byte("b"), 0o644)
		e.waitFor("barrier "+p, func() bool { return len(e.history(p)) > 0 })
	}
}

func (e *env) want(p string, want ...string) {
	e.t.Helper()
	if got := e.history(p); strings.Join(got, " | ") != strings.Join(want, " | ") {
		e.t.Errorf("%s:\n got %q\nwant %q", p, got, want)
	}
}

func put(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, mode)
}

// Each writer gives exactly the expected rows, and temp names none (11.3).
func TestWriterMatrix(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	for _, n := range []string{"sed", "nano", "install", "visudo", "dpkg", "vim", "touch", "rm"} {
		put(t, r(n), n+" v1\n", 0o644)
	}
	put(t, r("chmod"), "#!/bin/sh\n", 0o755)
	os.Symlink("/old/target", r("lnk"))
	os.Mkdir(r("cons"), 0o755)
	e.start()

	// sed -i: a temp name renamed over the file.
	put(t, r("sedAbC123"), "sed v2\n", 0o644)
	os.Rename(r("sedAbC123"), r("sed"))
	// nano, cp, tee: truncate, write half, pause, write the rest.
	f, _ := os.OpenFile(r("nano"), os.O_WRONLY|os.O_TRUNC, 0)
	f.WriteString("nano v2 first half, ")
	time.Sleep(20 * time.Millisecond)
	f.WriteString("second half\n")
	f.Close()
	// install: unlink, create 0600, write, close, chmod 0644.
	os.Remove(r("install"))
	put(t, r("install"), "install v2\n", 0o600)
	os.Chmod(r("install"), 0o644)
	// mv in from another directory on the same file system.
	other := filepath.Join(filepath.Dir(e.root), "elsewhere")
	os.Mkdir(other, 0o755)
	put(t, filepath.Join(other, "mvin"), "moved in\n", 0o644)
	os.Rename(filepath.Join(other, "mvin"), r("mvin"))
	// visudo: x.tmp renamed over x.
	put(t, r("visudo.tmp"), "visudo v2\n", 0o440)
	os.Rename(r("visudo.tmp"), r("visudo"))
	// dpkg: x.dpkg-new over x; a directory d.dpkg-new renamed to d.
	put(t, r("dpkg.dpkg-new"), "dpkg v2\n", 0o644)
	os.Rename(r("dpkg.dpkg-new"), r("dpkg"))
	os.Mkdir(r("d.dpkg-new"), 0o755)
	put(t, r("d.dpkg-new/a"), "a\n", 0o644)
	put(t, r("d.dpkg-new/b"), "b\n", 0o644)
	os.Rename(r("d.dpkg-new"), r("d"))
	// vim: x renamed to x~, a new x written.
	os.Rename(r("vim"), r("vim~"))
	put(t, r("vim"), "vim v2\n", 0o644)
	// ln -sf: a temp link renamed over.
	os.Symlink("/new/target", r("lnk.tmp"))
	os.Rename(r("lnk.tmp"), r("lnk"))
	os.Chmod(r("chmod"), 0o644)
	later := time.Now().Add(time.Hour)
	os.Chtimes(r("touch"), later, later)
	os.Remove(r("rm"))
	// Never recorded: editor swap, dpkg leftovers, backups.
	put(t, r(".x.swp"), "swap", 0o644)
	put(t, r("x.dpkg-old"), "old", 0o644)
	put(t, r("x~"), "backup", 0o644)
	// A consumer directory records backup names, but not dpkg staging.
	put(t, r("cons/x.bak"), "bak", 0o644)
	put(t, r("cons/x.dpkg-new"), "staging", 0o644)
	e.barrier()

	e.want(r("sed"), "file first seen", "file changed")
	e.want(r("nano"), "file first seen", "file changed")
	if c := e.newest(r("nano")); c.Size != int64(len("nano v2 first half, second half\n")) {
		t.Errorf("nano row has %d bytes", c.Size)
	}
	if c := e.newest(r("install")); c.Mode != 0o644 {
		t.Errorf("install row mode %v", c.Mode)
	}
	if h := e.history(r("install")); strings.Contains(strings.Join(h, "|"), "deleted") {
		t.Errorf("install: %q", h)
	}
	e.want(r("mvin"), "deleted did not exist", "file created")
	e.want(r("visudo"), "file first seen", "file changed, mode 0644->0440")
	e.want(r("dpkg"), "file first seen", "file changed")
	e.want(r("d/a"), "deleted did not exist", "file created")
	e.want(r("d/b"), "deleted did not exist", "file created")
	e.want(r("vim"), "file first seen", "file changed")
	e.want(r("lnk"), "link first seen", "link link -> /new/target")
	e.want(r("chmod"), "file first seen", "file mode 0755->0644")
	e.want(r("touch"), "file first seen")
	e.want(r("rm"), "file first seen", "deleted deleted")
	e.want(r("cons/x.bak"), "deleted did not exist", "file created")
	for _, n := range []string{"sedAbC123", "visudo.tmp", "dpkg.dpkg-new", "d.dpkg-new/a", "vim~", "lnk.tmp",
		".x.swp", "x.dpkg-old", "x~", "cons/x.dpkg-new"} {
		e.want(r(n))
	}
	// A later look at the removed file adds nothing.
	e.w.requestRescan()
	e.barrier()
	e.want(r("rm"), "file first seen", "deleted deleted")
}

// Structure: new directories, moved directories, changes while stopped,
// fingerprints and login homes (11.4).
func TestStructural(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	os.Mkdir(r("dir"), 0o755)
	put(t, r("dir/f"), "f\n", 0o644)
	put(t, r("keep"), "1\n", 0o644)
	put(t, r("gone"), "1\n", 0o644)
	put(t, r("secret"), "PRIVATE\n", 0o600)
	put(t, filepath.Join(e.user, ".ssh/authorized_keys"), "ssh-ed25519 AAAA\n", 0o600)
	put(t, filepath.Join(e.user, ".ssh/id_x"), "PRIVATE KEY\n", 0o600)
	e.start()

	// 50 new directories, each with a file written at once.
	for i := 0; i < 50; i++ {
		d := r(fmt.Sprintf("new%02d", i))
		os.Mkdir(d, 0o755)
		put(t, filepath.Join(d, "f"), "x\n", 0o644)
	}
	os.Rename(r("dir"), r("dir2"))
	e.barrier()
	for i := 0; i < 50; i++ {
		e.want(r(fmt.Sprintf("new%02d/f", i)), "deleted did not exist", "file created")
	}
	e.want(r("dir/f"), "file first seen", "deleted deleted")
	e.want(r("dir2/f"), "deleted did not exist", "file created")

	// The fingerprint rule: a digest row, no object.
	c := e.newest(r("secret"))
	if c.Kind != store.KindDigest {
		t.Errorf("secret row %+v", c)
	}
	if _, err := os.Stat(filepath.Join(e.home, "objects", c.Blob[:2], c.Blob)); !os.IsNotExist(err) {
		t.Error("secret content stored")
	}
	// Login roots: authorized_keys recorded, private keys not.
	e.want(filepath.Join(e.user, ".ssh/authorized_keys"), "file first seen")
	e.want(filepath.Join(e.user, ".ssh/id_x"))

	// Changes while stopped are found at the next start.
	e.stop()
	put(t, r("keep"), "2\n", 0o644)
	os.Remove(r("gone"))
	e.log = &syncBuf{}
	e.cfg.Log = e.log
	e.start()
	e.want(r("keep"), "file first seen", "file changed while not watching")
	e.want(r("gone"), "file first seen", "deleted deleted while not watching")
	if !strings.Contains(e.log.String(), "baseline: 0 first seen, 1 changed and 1 deleted while not watching") {
		t.Errorf("log:\n%s", e.log.String())
	}
}

// A sed-style rename and an rm plus recreate of files the walk listed are
// never "did not exist"; a file created after the listing is (6.4).
func TestProofOfAbsence(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	for i := 0; i < 20; i++ {
		put(t, r(fmt.Sprintf("f%02d", i)), "x\n", 0o644)
	}
	release := make(chan struct{})
	var once sync.Once
	testHookBeforeRecord = func() { once.Do(func() { <-release }) }
	// Reset after the watcher stops: cleanups run last-registered first.
	t.Cleanup(func() { testHookBeforeRecord = nil })
	w, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.w = w
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan error, 1)
	go func() { e.done <- w.Run(ctx) }()
	t.Cleanup(e.stop)
	// Wait until the walk has listed the directory (the worker is stalled).
	e.waitFor("the walk", func() bool { return strings.Contains(e.log.String(), "watching ") })
	put(t, r("sedXYZ123"), "y\n", 0o644)
	os.Rename(r("sedXYZ123"), r("f03"))
	os.Remove(r("f07"))
	put(t, r("f07"), "z\n", 0o644)
	put(t, r("fresh"), "new\n", 0o644)
	time.Sleep(100 * time.Millisecond) // let the reader handle the events
	close(release)
	st, err := store.Open(e.home)
	if err != nil {
		t.Fatal(err)
	}
	e.st = st
	defer st.Close()
	e.waitFor("the baseline", func() bool { return strings.Contains(e.log.String(), "baseline: ") })
	e.barrier()
	for i := 0; i < 20; i++ {
		e.want(r(fmt.Sprintf("f%02d", i)), "file first seen")
	}
	e.want(r("fresh"), "deleted did not exist", "file created")
}

// Every walk replaces its directory's listing (6.4): a name that is gone
// leaves it, so a file made under that name later keeps its proof of
// absence; a name that is still there stays (final review).
func TestWalkTrimsListing(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	put(t, r("kept"), "k\n", 0o644)
	var n atomic.Int32
	testHookRescan = func(string) { n.Add(1) }
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	os.WriteFile(r("x"), []byte("x\n"), 0o644) // gone before it is read
	os.Remove(r("x"))
	e.barrier()
	e.want(r("x"))
	start := n.Load()
	e.w.requestRescan()
	e.waitFor("the rescan", func() bool { return n.Load() > start })
	e.barrier()
	e.w.mu.Lock()
	list := e.w.listings[e.root]
	gone, kept := list["x"], list["kept"]
	e.w.mu.Unlock()
	if gone || !kept {
		t.Fatalf("listing after the walk: x %v, kept %v", gone, kept)
	}
	put(t, r("x"), "x again\n", 0o644)
	e.waitFor("x", func() bool { return len(e.history(r("x"))) > 0 })
	e.want(r("x"), "deleted did not exist", "file created")
}

// A temp file that a killed sc restore left next to its target is reported
// once by the walks, and left alone: never removed, never recorded. A
// fresh one may be a restore at work and is not reported.
func TestStaleTempReported(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	old, fresh := r(".hosts.sc-tmp-123"), r(".conf.sc-tmp-456")
	put(t, old, "half a restore\n", 0o600)
	hour := time.Now().Add(-time.Hour)
	os.Chtimes(old, hour, hour)
	put(t, fresh, "a restore at work\n", 0o600)
	var n atomic.Int32
	testHookRescan = func(string) { n.Add(1) }
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	start := n.Load()
	e.w.requestRescan()
	e.waitFor("the rescan", func() bool { return n.Load() > start })
	e.barrier()
	log := e.log.String()
	if n := strings.Count(log, "stale temp file "+old); n != 1 {
		t.Fatalf("reported %d times:\n%s", n, log)
	}
	if strings.Contains(log, fresh) {
		t.Fatalf("fresh temp file reported:\n%s", log)
	}
	if _, err := os.Lstat(old); err != nil {
		t.Fatal("removed:", err)
	}
	e.want(old)
	e.want(fresh)
}

// A restore of a file, a link or a "did not exist" row leaves the restore
// and pre-restore rows newest, with no auto row after them.
func TestRestoreWhileWatching(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	put(t, r("conf"), "good\n", 0o644)
	os.Symlink("/good", r("unit"))
	processed := make(chan string, 100)
	testHookProcessed = func(p string) {
		select {
		case processed <- p:
		default:
		}
	}
	// Reset after the watcher stops: cleanups run last-registered first.
	t.Cleanup(func() { testHookProcessed = nil })
	e.start()
	waitProcessed := func(p string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case q := <-processed:
				if q == p {
					return
				}
			case <-deadline:
				t.Fatalf("%s not processed", p)
			}
		}
	}
	goodConf := e.newest(r("conf"))
	goodUnit := e.newest(r("unit"))
	put(t, r("new"), "x\n", 0o644)
	e.waitFor("new", func() bool { return len(e.history(r("new"))) == 2 })
	didNotExist, _ := e.st.List(r("new"), 0)
	gone := didNotExist[1]

	check := func(p string) {
		t.Helper()
		cs, _ := e.st.List(p, 2)
		if cs[0].Origin != store.OriginRestore || cs[1].Origin != store.OriginPreRestore {
			t.Fatalf("%s newest rows: %+v", p, cs)
		}
	}
	for i := 0; i < 20; i++ {
		put(t, r("conf"), fmt.Sprintf("broken %d\n", i), 0o644)
		waitProcessed(r("conf"))
		if _, _, err := e.st.Restore(goodConf.ID); err != nil {
			t.Fatal(err)
		}
		waitProcessed(r("conf"))
		check(r("conf"))

		os.Remove(r("unit"))
		os.Symlink(fmt.Sprintf("/broken%d", i), r("unit"))
		waitProcessed(r("unit"))
		if _, _, err := e.st.Restore(goodUnit.ID); err != nil {
			t.Fatal(err)
		}
		waitProcessed(r("unit"))
		check(r("unit"))

		put(t, r("new"), fmt.Sprintf("again %d\n", i), 0o644)
		waitProcessed(r("new"))
		if _, _, err := e.st.Restore(gone.ID); err != nil {
			t.Fatal(err)
		}
		waitProcessed(r("new"))
		check(r("new"))
	}
}

// Restores while the watcher records a 2,000-file baseline in another
// root never fail with "database is locked".
func TestRestoreDuringBaseline(t *testing.T) {
	e := newEnv(t)
	big := filepath.Join(filepath.Dir(e.root), "big")
	os.Mkdir(big, 0o755)
	for i := 0; i < 2000; i++ {
		put(t, filepath.Join(big, fmt.Sprintf("f%04d", i)), "x\n", 0o644)
	}
	e.cfg.ScopeText += "root " + big + "\n"
	conf := filepath.Join(e.root, "conf")
	put(t, conf, "good\n", 0o644)
	// A row to restore, written before the watcher starts.
	if err := store.Init(e.home); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(e.home)
	if err != nil {
		t.Fatal(err)
	}
	e.st = st
	t.Cleanup(func() { st.Close() })
	good, _, err := st.Snapshot(conf, store.OriginManual, "good")
	if err != nil {
		t.Fatal(err)
	}
	w, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan error, 1)
	go func() { e.done <- w.Run(ctx) }()
	t.Cleanup(e.stop)
	for i := 0; i < 20; i++ {
		put(t, conf, fmt.Sprintf("bad %d\n", i), 0o644)
		if _, _, err := st.Restore(good.ID); err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
	}
	// 2,000 fsynced blobs take a while, more so under the race detector.
	e.waitForWithin(60*time.Second, "the baseline", func() bool { return strings.Contains(e.log.String(), "baseline: ") })
	if n := len(e.history(filepath.Join(big, "f1999"))); n != 1 {
		t.Fatalf("big root not recorded: %d rows; log:\n%s", n, e.log.String())
	}
}

// Content never reaches the log; lines name the tier, path, change and id.
func TestLogNeverHasContent(t *testing.T) {
	e := newEnv(t)
	fstab := filepath.Join(e.root, "fstab")
	put(t, fstab, "MARKER-7f3a-before\n", 0o644)
	e.start()
	put(t, fstab, "MARKER-7f3a-after\n", 0o600)
	e.barrier()
	// The row commits before its line is written: wait for the line.
	c := e.newest(fstab)
	want := fmt.Sprintf("<4>T1 %s: changed, mode 0644->0600 (%s)\n", fstab, c.ID)
	e.waitFor("the log line", func() bool { return strings.Contains(e.log.String(), want) })
	if log := e.log.String(); strings.Contains(log, "MARKER") {
		t.Fatalf("content logged:\n%s", log)
	}
}

func TestPanicEndsRun(t *testing.T) {
	e := newEnv(t)
	// Hooks are set before the watcher starts and armed afterwards, so the
	// test never writes a variable a running goroutine reads.
	var armed atomic.Bool
	testHookPanic = func() {
		if armed.Load() {
			panic("injected")
		}
	}
	// Reset after the watcher stops: cleanups run last-registered first.
	t.Cleanup(func() { testHookPanic = nil })
	e.start()
	armed.Store(true)
	e.w.wake()
	select {
	case err := <-e.done:
		if err == nil || !strings.Contains(err.Error(), "internal error in the worker: injected") {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a panic")
	}
	e.cancel = nil
}

func TestSecondInstanceFails(t *testing.T) {
	e := newEnv(t)
	e.start()
	_, err := New(e.cfg)
	if err == nil || err.Error() != fmt.Sprintf("scd already running on %s (pid %d)", e.home, os.Getpid()) {
		t.Fatalf("second New: %v", err)
	}
}

func TestHomeChecks(t *testing.T) {
	e := newEnv(t)
	// SC_HOME inside a root: refused before anything is created.
	inside := filepath.Join(e.root, "sub", "home")
	cfg := e.cfg
	cfg.Home = inside
	_, err := New(cfg)
	if err == nil || err.Error() != "SC_HOME "+inside+" is inside watched root "+e.root {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(e.root, "sub")); !os.IsNotExist(err) {
		t.Fatal("something was created")
	}
	// Through a symlink to the root: still refused.
	link := filepath.Join(filepath.Dir(e.root), "rootlink")
	os.Symlink(e.root, link)
	cfg.Home = filepath.Join(link, "home")
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "is inside watched root") {
		t.Fatalf("via symlink: %v", err)
	}
	// A missing SC_HOME is created 0700.
	cfg.Home = filepath.Join(filepath.Dir(e.root), "a", "b", "home")
	w, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.close()
	if fi, err := os.Stat(cfg.Home); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("SC_HOME %v %v", fi, err)
	}
}

// A login .ssh that is a symlink (to /) is not watched and is logged once.
func TestSymlinkedSSHRoot(t *testing.T) {
	e := newEnv(t)
	ssh := filepath.Join(e.user, ".ssh")
	os.RemoveAll(ssh)
	os.Symlink("/", ssh)
	e.start()
	e.w.requestRescan()
	e.barrier()
	e.w.requestRescan()
	e.barrier()
	log := e.log.String()
	if n := strings.Count(log, "not watching "+ssh+": it is a symlink"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, log)
	}
	e.w.mu.Lock()
	_, watched := e.w.dirs[ssh]
	e.w.mu.Unlock()
	if watched {
		t.Fatal("symlinked .ssh watched")
	}
}

// A user who swaps their ~/.ssh for a symlink to a directory holding
// SC_HOME, after the startup lstat and before the SC_HOME check, cannot
// fail the start: the startup rescan skips the root and logs it.
func TestSSHRootSwappedAtStartup(t *testing.T) {
	e := newEnv(t)
	ssh := filepath.Join(e.user, ".ssh")
	testHookRootsListed = func() {
		os.RemoveAll(ssh)
		os.Symlink(filepath.Dir(e.home), ssh)
	}
	t.Cleanup(func() { testHookRootsListed = nil })
	e.start()
	if log := e.log.String(); !strings.Contains(log, "not watching "+ssh+": it is a symlink") {
		t.Fatalf("not logged:\n%s", log)
	}
	e.w.mu.Lock()
	_, watched := e.w.dirs[ssh]
	e.w.mu.Unlock()
	if watched {
		t.Fatal("symlinked .ssh watched")
	}
}

var _ = fsutil.ReadState
var _ = syscall.IN_CREATE

// A restore between the watcher's read and its Record leaves no stale
// auto row after the restore row: the stamp check under the lock sees the
// path moved (plan 5.5).
func TestRestoreBetweenReadAndRecord(t *testing.T) {
	e := newEnv(t)
	conf := filepath.Join(e.root, "conf")
	put(t, conf, "good\n", 0o644)
	var armed atomic.Bool
	var restoreID atomic.Value
	restored := make(chan error, 1)
	testHookBeforeStoreRecord = func() {
		if armed.CompareAndSwap(true, false) {
			st, err := store.Open(e.home)
			if err == nil {
				_, _, err = st.Restore(restoreID.Load().(string))
				st.Close()
			}
			restored <- err
		}
	}
	// Reset after the watcher stops: cleanups run last-registered first.
	t.Cleanup(func() { testHookBeforeStoreRecord = nil })
	e.start()
	restoreID.Store(e.newest(conf).ID)
	armed.Store(true)
	put(t, conf, "broken\n", 0o644) // read by the watcher, then restored away
	if err := <-restored; err != nil {
		t.Fatal(err)
	}
	e.barrier()
	cs, _ := e.st.List(conf, 0)
	if cs[0].Origin != store.OriginRestore {
		t.Fatalf("newest rows: %+v", cs)
	}
	if b, _ := os.ReadFile(conf); string(b) != "good\n" {
		t.Fatalf("content %q", b)
	}
}

// Baseline "first seen" rows are summed up, not logged one by one.
func TestBaselineNotLoggedPerRow(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		put(t, filepath.Join(e.root, fmt.Sprintf("f%d", i)), "x\n", 0o644)
	}
	e.start()
	log := e.log.String()
	if strings.Contains(log, ": first seen") || !strings.Contains(log, "baseline: 5 first seen,") {
		t.Fatalf("log:\n%s", log)
	}
}

// A path that changes type gets the right rows (chunk D review): a file
// replaced by a directory, a directory by a symlink or a file, and a
// directory moved away with a file put at its name at once.
func TestTypeChanges(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	put(t, r("f"), "f\n", 0o644)
	for _, d := range []string{"tolink", "tofile", "moved"} {
		os.Mkdir(r(d), 0o755)
		put(t, r(d+"/x"), "x\n", 0o644)
	}
	e.start()
	os.Remove(r("f"))
	os.Mkdir(r("f"), 0o755)
	os.RemoveAll(r("tolink"))
	os.Symlink("/usr/share", r("tolink"))
	os.RemoveAll(r("tofile"))
	put(t, r("tofile"), "now a file\n", 0o644)
	os.Rename(r("moved"), filepath.Join(filepath.Dir(e.root), "moved-out"))
	put(t, r("moved"), "a file now\n", 0o644)
	for _, p := range []string{"f", "tolink/x", "tofile/x", "moved/x"} {
		p := r(p)
		e.waitFor(p+" deleted", func() bool {
			h := e.history(p)
			return len(h) == 2 && h[1] == "deleted deleted"
		})
	}
	e.want(r("tolink"), "link first seen")
	e.want(r("tofile"), "file first seen")
	e.want(r("moved"), "file first seen")
}

// The same type changes while scd is stopped are found at the next start,
// and the baseline completes.
func TestTypeChangesWhileStopped(t *testing.T) {
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	for _, d := range []string{"tolink", "tofile"} {
		os.Mkdir(r(d), 0o755)
		put(t, r(d+"/x"), "x\n", 0o644)
	}
	put(t, r("f"), "f\n", 0o644)
	e.start()
	e.stop()
	os.RemoveAll(r("tolink"))
	os.Symlink("/usr/share", r("tolink"))
	os.RemoveAll(r("tofile"))
	put(t, r("tofile"), "file\n", 0o644)
	os.Remove(r("f"))
	os.Mkdir(r("f"), 0o755)
	e.log = &syncBuf{}
	e.cfg.Log = e.log
	e.start()
	for _, p := range []string{"tolink/x", "tofile/x", "f"} {
		e.want(r(p), "file first seen", "deleted deleted while not watching")
	}
	if !strings.Contains(e.log.String(), "3 deleted while not watching") {
		t.Fatalf("log:\n%s", e.log.String())
	}
}

// A directory moved away whose files push the dirty set past MaxDirty
// clears it and rescans; it used to crash the worker (chunk D review).
func TestDirtyBoundFromMovedDir(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxDirty = 20
	d := filepath.Join(e.root, "d")
	os.Mkdir(d, 0o755)
	for i := 0; i < 30; i++ {
		put(t, filepath.Join(d, fmt.Sprintf("f%02d", i)), "x\n", 0o644)
	}
	e.start()
	os.Rename(d, filepath.Join(filepath.Dir(e.root), "away"))
	for i := 0; i < 30; i++ {
		p := filepath.Join(d, fmt.Sprintf("f%02d", i))
		e.waitFor(p+" deleted", func() bool { return len(e.history(p)) == 2 })
	}
}

// renameat2(RENAME_EXCHANGE) swaps two watched directories, with their
// subdirectories: all stay watched, so later edits in each are recorded
// from events, with no rescan (final review).
func TestDirExchange(t *testing.T) {
	nr, ok := map[string]uintptr{"amd64": 316, "arm64": 276}[runtime.GOARCH]
	if !ok {
		t.Skip("renameat2 number not known for " + runtime.GOARCH)
	}
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	os.MkdirAll(r("a/sub"), 0o755)
	os.MkdirAll(r("b/sub"), 0o755)
	put(t, r("a/sub/fa"), "a\n", 0o644)
	put(t, r("b/sub/fb"), "b\n", 0o644)
	var n atomic.Int32
	testHookRescan = func(string) { n.Add(1) }
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	start := n.Load()
	pa, _ := syscall.BytePtrFromString(r("a"))
	pb, _ := syscall.BytePtrFromString(r("b"))
	cwd := -100 // AT_FDCWD
	const exchange = 2
	if _, _, errno := syscall.Syscall6(nr, uintptr(cwd), uintptr(unsafe.Pointer(pa)),
		uintptr(cwd), uintptr(unsafe.Pointer(pb)), exchange, 0); errno != 0 {
		t.Skip("renameat2:", errno)
	}
	e.barrier()
	for _, p := range []string{"a/sub/fb", "b/sub/fa", "a/fb2", "b/fa2"} {
		put(t, r(p), "edit\n", 0o644)
	}
	for _, p := range []string{"a/sub/fb", "b/sub/fa", "a/fb2", "b/fa2"} {
		e.waitFor("the edit of "+p, func() bool {
			cs, err := e.st.List(r(p), 1)
			return err == nil && len(cs) == 1 && cs[0].Kind == store.KindFile && cs[0].Size == int64(len("edit\n"))
		})
	}
	if got := n.Load() - start; got != 0 {
		t.Fatalf("%d rescans: the edits must come from events", got)
	}
}

// A busy host whose scope holds more paths than MaxDirty: a rescan's marks
// do not count against the bound, so events meanwhile do not clear them
// and start rescan after rescan (chunk D concurrency review).
func TestRescanUnderBusyEvents(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxDirty = 100
	e.cfg.Batch = 10
	var files []string
	for i := 0; i < 300; i++ {
		p := filepath.Join(e.root, fmt.Sprintf("f%03d", i))
		put(t, p, "one\n", 0o644)
		files = append(files, p)
	}
	var n atomic.Int32
	testHookRescan = func(string) { n.Add(1) }
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	other := filepath.Join(filepath.Dir(e.root), "unwatched")
	os.Mkdir(other, 0o755)
	for i, p := range files { // changes no event reports (hard links)
		a := filepath.Join(other, fmt.Sprint(i))
		if err := os.Link(p, a); err != nil {
			t.Skip(err)
		}
		f, _ := os.OpenFile(a, os.O_WRONLY|os.O_APPEND, 0)
		f.WriteString("two\n")
		f.Close()
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() { // a new file every 10 ms
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			os.WriteFile(filepath.Join(e.root, fmt.Sprintf("new%05d", i)), nil, 0o644)
		}
	}()
	start := n.Load()
	e.w.requestRescan()
	for _, p := range files {
		p := p
		e.waitForWithin(30*time.Second, p, func() bool { return len(e.history(p)) == 2 })
	}
	if got := n.Load() - start; got > 2 {
		t.Fatalf("%d rescans for one request", got)
	}
}

// Stopping during a rescan ends the walk instead of calling add_watch on
// closed inotify instances (one error line per directory before).
func TestStopDuringRescan(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 20; i++ {
		os.Mkdir(filepath.Join(e.root, fmt.Sprintf("d%02d", i)), 0o755)
	}
	var armed atomic.Bool
	var cancel atomic.Value
	testHookRescan = func(string) {
		if armed.CompareAndSwap(true, false) {
			cancel.Load().(func())()
			time.Sleep(100 * time.Millisecond)
		}
	}
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	cancel.Store(e.cancel)
	armed.Store(true)
	e.w.requestRescan()
	if err := <-e.done; err != nil {
		t.Fatal(err)
	}
	e.cancel = nil
	if n := strings.Count(e.log.String(), "cannot watch"); n != 0 {
		t.Fatalf("%d error lines on stop:\n%s", n, e.log.String())
	}
}

// A panic while the worker holds mu still ends Run, even if a reader is
// left blocked on mu, so systemd can restart scd.
func TestPanicWhileLockedEndsRun(t *testing.T) {
	e := newEnv(t)
	var armed atomic.Bool
	testHookPanicLocked = func() {
		if armed.Load() {
			// Hold mu long enough for the busy reader to block on it.
			time.Sleep(300 * time.Millisecond)
			panic("injected with mu held")
		}
	}
	t.Cleanup(func() { testHookPanicLocked = nil })
	e.start()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // keep a reader busy, so it blocks on mu
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			os.WriteFile(filepath.Join(e.root, "busy"), []byte{byte(i)}, 0o644)
			time.Sleep(time.Millisecond)
		}
	}()
	armed.Store(true)
	e.w.wake()
	select {
	case err := <-e.done:
		if err == nil || !strings.Contains(err.Error(), "injected with mu held") {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after a panic with mu held")
	}
	e.cancel = nil
}

// With explicit roots (sc watch --root) the watcher reads no passwd at all:
// nothing in it may do a name lookup that could hang at boot.
func TestExplicitRootsReadNoPasswd(t *testing.T) {
	e := newEnv(t)
	e.cfg.Roots = []string{e.root}
	e.cfg.PasswdPath = filepath.Join(t.TempDir(), "missing-passwd")
	e.start()
	if strings.Contains(e.log.String(), "passwd") {
		t.Fatalf("passwd read:\n%s", e.log.String())
	}
}

func TestCheckHomeRootSlash(t *testing.T) {
	if err := checkHome("/tmp/x/home", []string{"/"}); err == nil {
		t.Fatal("SC_HOME under / accepted")
	}
}

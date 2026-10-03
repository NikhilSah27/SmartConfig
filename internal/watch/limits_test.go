//go:build linux

package watch

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"smartconfig/internal/store"
)

// stallReader returns a hook that, once armed, blocks the reader of the
// instance pick() returns until release is closed.
func stallReader(t *testing.T, pick func() *Inotify) (arm func(), release func()) {
	var armed atomic.Bool
	ch := make(chan struct{})
	testHookBeforeRead = func(in *Inotify) {
		if armed.Load() && in == pick() {
			<-ch
		}
	}
	t.Cleanup(func() { testHookBeforeRead = nil })
	var once atomic.Bool
	return func() { armed.Store(true) }, func() {
		if once.CompareAndSwap(false, true) {
			close(ch)
		}
	}
}

// flood alternates chmod on two files n times: identical consecutive
// events coalesce, alternating ones do not.
func flood(t *testing.T, a, b string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		p := a
		if i%2 == 1 {
			p = b
		}
		os.Chmod(p, os.FileMode(0o600+i%2*0o40))
	}
}

func queueLimit(t *testing.T) int {
	b, err := os.ReadFile("/proc/sys/fs/inotify/max_queued_events")
	if err != nil {
		t.Skip(err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if n > 100000 {
		t.Skipf("max_queued_events is %d", n)
	}
	return n
}

// A real overflow: events beyond the kernel queue are lost, the overflow
// is logged, and the rescan finds what no event reported.
func TestOverflow(t *testing.T) {
	limit := queueLimit(t)
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	put(t, r("a"), "a", 0o644)
	put(t, r("b"), "b", 0o644)
	var w atomic.Pointer[Watcher]
	arm, release := stallReader(t, func() *Inotify { return w.Load().sys })
	e.start()
	t.Cleanup(release) // runs before the stop, even if a wait fails
	w.Store(e.w)
	arm()
	os.WriteFile(r("poke"), nil, 0o644) // the reader returns once, then stalls
	time.Sleep(50 * time.Millisecond)
	flood(t, r("a"), r("b"), limit+4000)
	put(t, r("late"), "late\n", 0o644)
	release()
	e.waitFor("late", func() bool { return len(e.history(r("late"))) > 0 })
	e.want(r("late"), "file first seen (found by rescan)")
	if !strings.Contains(e.log.String(), "event queue of the system roots overflowed") {
		t.Fatalf("log:\n%s", e.log.String())
	}
}

// A flood on a login home overflows only the home instance; a change under
// the system root meanwhile is recorded from its own event.
func TestOverflowIsolation(t *testing.T) {
	limit := queueLimit(t)
	e := newEnv(t)
	ssh := filepath.Join(e.user, ".ssh")
	put(t, filepath.Join(ssh, "authorized_keys"), "k1", 0o600)
	put(t, filepath.Join(ssh, "authorized_keys2"), "k2", 0o600)
	var w atomic.Pointer[Watcher]
	arm, release := stallReader(t, func() *Inotify { return w.Load().home })
	e.cfg.UserFileGap = time.Millisecond
	e.start()
	t.Cleanup(release) // runs before the stop, even if a wait fails
	w.Store(e.w)
	arm()
	os.Chmod(filepath.Join(ssh, "authorized_keys"), 0o644) // the reader returns once, then stalls
	time.Sleep(50 * time.Millisecond)
	flood(t, filepath.Join(ssh, "authorized_keys"), filepath.Join(ssh, "authorized_keys2"), limit+4000)
	sys := filepath.Join(e.root, "during")
	put(t, sys, "x\n", 0o644)
	e.waitFor("the system change", func() bool { return len(e.history(sys)) > 0 })
	release()
	e.waitFor("the home overflow", func() bool {
		return strings.Contains(e.log.String(), "event queue of the home roots overflowed")
	})
	e.want(sys, "deleted did not exist", "file created")
	if strings.Contains(e.log.String(), "event queue of the system roots") {
		t.Fatalf("system instance overflowed:\n%s", e.log.String())
	}
}

// An overflow loses a directory's move away and the new directory made at
// its name. The rescan that watches the new one drops the old one's
// watch: events in the old directory are no longer filed under the new
// one, where a name created there entered the new one's listing and took
// the proof of absence from a later file of that name (final review).
func TestOverflowDropsStaleWatch(t *testing.T) {
	limit := queueLimit(t)
	e := newEnv(t)
	r := func(n string) string { return filepath.Join(e.root, n) }
	put(t, r("a"), "a", 0o644)
	put(t, r("b"), "b", 0o644)
	os.Mkdir(r("d"), 0o755)
	var w atomic.Pointer[Watcher]
	arm, release := stallReader(t, func() *Inotify { return w.Load().sys })
	e.start()
	t.Cleanup(release)
	w.Store(e.w)
	arm()
	os.WriteFile(r("poke"), nil, 0o644) // the reader returns once, then stalls
	time.Sleep(50 * time.Millisecond)
	flood(t, r("a"), r("b"), limit+4000)
	away := filepath.Join(filepath.Dir(e.root), "d-away")
	os.Rename(r("d"), away) // lost with the flood
	os.Mkdir(r("d"), 0o755)
	put(t, r("late"), "late\n", 0o644)
	release()
	e.waitFor("the rescan", func() bool { return len(e.history(r("late"))) > 0 })

	e.w.mu.Lock()
	for in, m := range e.w.wds {
		for wd, p := range m {
			if ref, ok := e.w.dirs[p]; !ok || ref.in != in || ref.wd != wd {
				t.Errorf("stale watch %d on %s", wd, p)
			}
		}
	}
	e.w.mu.Unlock()

	put(t, filepath.Join(away, "g"), "old\n", 0o644)
	e.barrier()
	put(t, r("d/g"), "new\n", 0o644)
	e.waitFor("d/g", func() bool { return len(e.history(r("d/g"))) > 0 })
	e.want(r("d/g"), "deleted did not exist", "file created")
}

// With a short RescanEvery, a write through a hard link from a directory
// nobody watches (no event) is found by the periodic rescan.
func TestPeriodicRescan(t *testing.T) {
	e := newEnv(t)
	conf := filepath.Join(e.root, "conf")
	put(t, conf, "one\n", 0o644)
	e.cfg.RescanEvery = 200 * time.Millisecond
	e.start()
	other := filepath.Join(filepath.Dir(e.root), "unwatched")
	os.Mkdir(other, 0o755)
	if err := os.Link(conf, filepath.Join(other, "alias")); err != nil {
		t.Skip(err)
	}
	f, _ := os.OpenFile(filepath.Join(other, "alias"), os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString("two\n")
	f.Close()
	e.waitFor("the rescan", func() bool { return len(e.history(conf)) == 2 })
	e.want(conf, "file first seen", "file changed (found by rescan)")
}

// Requests within RescanMinGap of a rescan are merged into one.
func TestRescanRateLimit(t *testing.T) {
	e := newEnv(t)
	e.cfg.RescanMinGap = 2 * time.Second
	var n atomic.Int32
	testHookRescan = func(string) { n.Add(1) }
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	time.Sleep(2100 * time.Millisecond)
	start := n.Load()
	e.w.requestRescan()
	e.waitFor("one rescan", func() bool { return n.Load() == start+1 })
	// Ten requests spread over about 250 ms, well inside the 2 s gap,
	// give one more rescan, after the gap.
	begin := time.Now()
	for i := 0; i < 10; i++ {
		e.w.requestRescan()
		time.Sleep(25 * time.Millisecond)
	}
	if time.Since(begin) > 1500*time.Millisecond {
		t.Skip("machine too slow to stay inside the gap")
	}
	e.waitFor("the merged rescan", func() bool { return n.Load() >= start+2 })
	time.Sleep(300 * time.Millisecond)
	if got := n.Load() - start; got != 2 {
		t.Fatalf("%d rescans, want 2", got)
	}
}

// A root that keeps moving (a user renaming ~/.ssh away and back in a
// loop) delays a rescan by Cap once, not for as long as the loop runs: a
// change only a rescan can see (a write through a hard link) is found
// while the loop is still going.
func TestRootMovesCannotStarveRescans(t *testing.T) {
	e := newEnv(t)
	conf := filepath.Join(e.root, "conf")
	put(t, conf, "v1\n", 0o644)
	outside := filepath.Join(filepath.Dir(e.root), "outside-link")
	if err := os.Link(conf, outside); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	testHookRescan = func(string) { n.Add(1) }
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	e.waitFor("conf", func() bool { return len(e.history(conf)) == 1 })

	stop := make(chan struct{})
	done := make(chan struct{})
	ssh, away := filepath.Join(e.user, ".ssh"), filepath.Join(e.user, ".x")
	go func() { // a move every 200 ms, well inside Cap (1 s)
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			os.Rename(ssh, away)
			os.Rename(away, ssh)
		}
	}()
	defer func() { close(stop); <-done }()
	start := n.Load()
	f, err := os.OpenFile(outside, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("v2 through a hard link\n")
	f.Close()
	e.waitForWithin(4*time.Second, "a rescan while the root keeps moving", func() bool {
		return len(e.history(conf)) == 2
	})
	if n.Load() == start {
		t.Fatal("conf changed without a rescan")
	}
	e.want(conf, "file first seen", "file changed (found by rescan)")
}

// Roots change: a new login in passwd, a root that appears late, and a
// root moved away and back.
func TestRootChanges(t *testing.T) {
	e := newEnv(t)
	passwd := filepath.Join(e.root, "passwd")
	os.Rename(e.cfg.PasswdPath, passwd)
	e.cfg.PasswdPath = passwd
	late := filepath.Join(filepath.Dir(e.root), "late")
	e.cfg.ScopeText += "root " + late + "\n"
	e.cfg.RescanMinGap = 500 * time.Millisecond
	var n atomic.Int32 // set before the watcher starts: no race with it
	testHookRescan = func(string) { n.Add(1) }
	t.Cleanup(func() { testHookRescan = nil })
	e.start()
	if !strings.Contains(e.log.String(), "not watching "+late) {
		t.Fatalf("missing root not logged:\n%s", e.log.String())
	}

	// A new login account: its .ssh becomes a root after passwd changes.
	v := filepath.Join(filepath.Dir(e.user), "v")
	os.MkdirAll(filepath.Join(v, ".ssh"), 0o700)
	put(t, filepath.Join(v, ".ssh", "authorized_keys"), "ssh-ed25519 V\n", 0o600)
	f, _ := os.OpenFile(passwd, os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString("v:x:1001:1001::" + v + ":/bin/bash\n")
	f.Close()
	e.waitFor("v's key", func() bool { return len(e.history(filepath.Join(v, ".ssh", "authorized_keys"))) > 0 })

	// A root that appears later.
	os.Mkdir(late, 0o755)
	put(t, filepath.Join(late, "f"), "x\n", 0o644)
	e.w.requestRescan()
	e.waitFor("the late root", func() bool { return len(e.history(filepath.Join(late, "f"))) > 0 })

	// The root moved away and back: a rescan follows, later edits count.
	// The rescan this asks for waits Cap (1 s here) for things to settle,
	// so the root is found back in place, whenever the last rescan ran.
	away := e.root + ".away"
	time.Sleep(600 * time.Millisecond) // the gap since the last rescan has passed
	start := n.Load()
	os.Rename(e.root, away)
	time.Sleep(100 * time.Millisecond)
	os.Rename(away, e.root)
	e.waitFor("the rescan after the move", func() bool { return n.Load() > start })
	put(t, filepath.Join(e.root, "after"), "x\n", 0o644)
	e.waitFor("an edit after the move", func() bool { return len(e.history(filepath.Join(e.root, "after"))) > 0 })
}

// A login .ssh moved away gives its files deleted rows at the rescan that
// follows, and so does one swapped for a symlink; a new .ssh brings them
// back as new files.
func TestLoginRootGone(t *testing.T) {
	e := newEnv(t)
	ssh := filepath.Join(e.user, ".ssh")
	keys := filepath.Join(ssh, "authorized_keys")
	put(t, keys, "ssh-ed25519 A\n", 0o600)
	e.start()
	os.Rename(ssh, ssh+".old")
	e.waitFor("the deleted row", func() bool { return len(e.history(keys)) == 2 })
	e.want(keys, "file first seen", "deleted deleted (found by rescan)")

	os.Mkdir(ssh, 0o700)
	put(t, keys, "ssh-ed25519 B\n", 0o600)
	e.w.requestRescan()
	e.waitFor("the new key", func() bool { return len(e.history(keys)) == 3 })

	os.Rename(ssh, ssh+".new")
	os.Symlink(ssh+".new", ssh)
	e.waitFor("the deleted row through a symlink", func() bool { return len(e.history(keys)) == 4 })
	e.want(keys, "file first seen", "deleted deleted (found by rescan)",
		"file created (found by rescan)", "deleted deleted (found by rescan)")
}

// While scd is down, a login .ssh moved away gives deleted rows at start.
// A scope root moved away gives none: a separate /boot may only be
// unmounted (mount tracking is M4).
func TestRootGoneAtStartup(t *testing.T) {
	e := newEnv(t)
	ssh := filepath.Join(e.user, ".ssh")
	keys, conf := filepath.Join(ssh, "authorized_keys"), filepath.Join(e.root, "conf")
	put(t, keys, "ssh-ed25519 A\n", 0o600)
	put(t, conf, "x\n", 0o644)
	e.start()
	e.stop()
	os.Rename(ssh, ssh+".old")
	os.Rename(e.root, e.root+".away")
	e.start()
	e.waitFor("the second baseline", func() bool { return strings.Count(e.log.String(), "baseline: ") == 2 })
	e.want(keys, "file first seen", "deleted deleted while not watching")
	e.want(conf, "file first seen")
	if !strings.Contains(e.log.String(), "baseline: 0 first seen, 0 changed and 1 deleted while not watching") {
		t.Fatalf("baseline:\n%s", e.log.String())
	}
}

// add_watch fails (ENOSPC) for one directory: logged once, and a change
// there is found by the next rescan.
func TestWatchLimit(t *testing.T) {
	e := newEnv(t)
	sub := filepath.Join(e.root, "sub")
	os.Mkdir(sub, 0o755)
	testHookAddWatch = func(dir string) error {
		if dir == sub {
			return syscall.ENOSPC
		}
		return nil
	}
	t.Cleanup(func() { testHookAddWatch = nil })
	e.start()
	put(t, filepath.Join(sub, "f"), "x\n", 0o644)
	e.barrier()
	e.want(filepath.Join(sub, "f"))
	e.w.requestRescan()
	e.waitFor("the rescan", func() bool { return len(e.history(filepath.Join(sub, "f"))) > 0 })
	e.want(filepath.Join(sub, "f"), "file first seen (found by rescan)")
	if n := strings.Count(e.log.String(), "cannot watch "+sub+": no space left on device"); n != 2 {
		t.Fatalf("logged %d times over 2 rescans:\n%s", n, e.log.String())
	}
}

// Under the free-space floor, new content waits and one line says so;
// a mode-only change still goes through; afterwards it is recorded.
func TestLowSpace(t *testing.T) {
	e := newEnv(t)
	conf, mode := filepath.Join(e.root, "conf"), filepath.Join(e.root, "mode")
	put(t, conf, "one\n", 0o644)
	put(t, mode, "m\n", 0o644)
	var free atomic.Uint64
	free.Store(1 << 40)
	testHookStatfs = func() uint64 { return free.Load() }
	t.Cleanup(func() { testHookStatfs = nil })
	secret, link := filepath.Join(e.root, "secret"), filepath.Join(e.root, "unit")
	put(t, secret, "KEY 1\n", 0o600)
	e.start()
	free.Store(50 << 20)
	put(t, conf, "two\n", 0o644)
	os.Chmod(mode, 0o600)
	// Rows that store no new content still go through: a new link and a
	// fingerprint-only file with new content.
	os.Symlink("/dev/null", link)
	put(t, secret, "KEY 2\n", 0o600)
	e.waitFor("the mode row", func() bool { return len(e.history(mode)) == 2 })
	e.waitFor("the link row", func() bool { return len(e.history(link)) > 0 })
	e.waitFor("the digest row", func() bool { return len(e.history(secret)) == 2 })
	time.Sleep(500 * time.Millisecond) // several FloorBackoff rounds; a barrier would wait too
	e.want(conf, "file first seen")
	if n := strings.Count(e.log.String(), "MB free under"); n != 1 {
		t.Fatalf("%d low-space lines:\n%s", n, e.log.String())
	}
	free.Store(1 << 40)
	e.waitFor("the content row", func() bool { return len(e.history(conf)) == 2 })
	e.waitFor("the recovery line", func() bool { return strings.Contains(e.log.String(), "free space is back") })
}

// A store error is logged and retried; the change is recorded once the
// store works again.
func TestStoreErrorBackoff(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a 0500 directory")
	}
	e := newEnv(t)
	conf := filepath.Join(e.root, "conf")
	put(t, conf, "one\n", 0o644)
	e.start()
	objects := filepath.Join(e.home, "objects")
	os.Chmod(objects, 0o500)
	t.Cleanup(func() { os.Chmod(objects, 0o700) })
	put(t, conf, "two, new content\n", 0o644)
	e.waitFor("the error line", func() bool { return strings.Contains(e.log.String(), "store: ") })
	os.Chmod(objects, 0o700)
	e.waitFor("the row", func() bool { return len(e.history(conf)) == 2 })
}

// A user-owned file in a login .ssh over UserFileMax gives a digest row,
// and a second change within UserFileGap waits for the gap.
func TestUserFileLimits(t *testing.T) {
	e := newEnv(t)
	keys := filepath.Join(e.user, ".ssh", "authorized_keys")
	put(t, keys, "short\n", 0o600)
	if os.Geteuid() == 0 {
		os.Chown(keys, 1000, 1000)
	}
	e.cfg.UserFileMax = 100
	e.cfg.UserFileGap = 600 * time.Millisecond
	e.start()
	put(t, keys, strings.Repeat("ssh-ed25519 AAAA\n", 20), 0o600)
	e.waitFor("the digest row", func() bool { return len(e.history(keys)) == 2 })
	if c := e.newest(keys); c.Kind != store.KindDigest {
		t.Fatalf("over the limit: %+v", c)
	}
	start := time.Now()
	put(t, keys, "short again\n", 0o600)
	e.waitFor("the gap", func() bool { return len(e.history(keys)) == 3 })
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("second row after %v, before the gap", d)
	}
}

// A home file rewritten between its read and its record leaves its new
// object stored with no row. That object counts against UserFileGap like
// a row: a user rewriting their .ssh files without pause adds one object
// per file per gap, not one per retry (final review).
func TestUserFileOrphansLimited(t *testing.T) {
	e := newEnv(t)
	keys := filepath.Join(e.user, ".ssh", "authorized_keys") // made later: no row starts the gap
	e.cfg.UserFileGap = time.Minute
	var armed atomic.Bool
	var tries atomic.Int32
	testHookBeforeStoreRecord = func() {
		if armed.Load() { // the record finds keys moved
			os.WriteFile(keys, []byte(fmt.Sprintf("k%d\n", tries.Add(1))), 0o600)
		}
	}
	t.Cleanup(func() { testHookBeforeStoreRecord = nil })
	e.start()
	objects := func() int {
		n := 0
		filepath.Walk(filepath.Join(e.home, "objects"), func(_ string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() {
				n++
			}
			return nil
		})
		return n
	}
	before := objects()
	armed.Store(true)
	put(t, keys, "rewritten\n", 0o600)
	e.waitFor("the first try", func() bool { return tries.Load() > 0 })
	time.Sleep(1500 * time.Millisecond) // 30 quiet periods
	armed.Store(false)
	if n := tries.Load(); n > 1 {
		t.Fatalf("%d tries within the gap", n)
	}
	if n := objects() - before; n > 1 {
		t.Fatalf("%d new objects within the gap", n)
	}
}

// A system file rewritten without pause gets PathBurst rows, then one per
// PathGap: the newest state, marked (rate-limited), with one warning line
// (follow-up 8, your pick).
func TestPathRateLimit(t *testing.T) {
	e := newEnv(t)
	conf := filepath.Join(e.root, "conf")
	put(t, conf, "v0\n", 0o644)
	e.cfg.PathBurst, e.cfg.PathGap = 3, 4*time.Second // longer than a stall of this VM
	e.start()                                         // the first seen row spends the first of 3
	for i := 1; i <= 2; i++ {
		put(t, conf, fmt.Sprintf("v%d\n", i), 0o644)
		e.waitFor(fmt.Sprintf("v%d", i), func() bool { return len(e.history(conf)) == i+1 })
	}
	put(t, conf, "v3\n", 0o644) // none left: waits
	time.Sleep(200 * time.Millisecond)
	put(t, conf, "v4, the newest\n", 0o644)
	e.waitForWithin(15*time.Second, "the limited row", func() bool { return len(e.history(conf)) == 4 })
	e.want(conf, "file first seen", "file changed", "file changed", "file changed (rate-limited)")
	if c := e.newest(conf); c.Size != int64(len("v4, the newest\n")) {
		t.Fatalf("not the newest state: %+v", c)
	}
	if n := strings.Count(e.log.String(), conf+" changes constantly"); n != 1 {
		t.Fatalf("warned %d times:\n%s", n, e.log.String())
	}
}

// More than MaxDirty marks clear the set and request one rescan, which
// finds every file.
func TestDirtyBound(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxDirty = 20
	e.cfg.Quiet = 300 * time.Millisecond // keep the marks waiting
	e.start()
	for i := 0; i < 30; i++ {
		put(t, filepath.Join(e.root, fmt.Sprintf("f%02d", i)), "x\n", 0o644)
	}
	e.waitFor("the bound", func() bool { return strings.Contains(e.log.String(), "more than 20 changes waiting") })
	for i := 0; i < 30; i++ {
		p := filepath.Join(e.root, fmt.Sprintf("f%02d", i))
		e.waitFor(p, func() bool { return len(e.history(p)) > 0 })
	}
}

// A manual row for a file outside every root is never read by a rescan and
// gets no deleted row when the file goes.
func TestOutsideRootsNeverRead(t *testing.T) {
	e := newEnv(t)
	out := filepath.Join(filepath.Dir(e.root), "outside")
	put(t, out, "x\n", 0o644)
	var seen atomic.Bool
	testHookProcessed = func(p string) {
		if p == out {
			seen.Store(true)
		}
	}
	t.Cleanup(func() { testHookProcessed = nil })
	e.start()
	if _, _, err := e.st.Snapshot(out, store.OriginManual, "by hand"); err != nil {
		t.Fatal(err)
	}
	os.Remove(out)
	e.w.requestRescan()
	time.Sleep(200 * time.Millisecond)
	e.w.requestRescan()
	e.barrier()
	if seen.Load() || len(e.history(out)) != 1 {
		t.Fatalf("outside path read: %v %q", seen.Load(), e.history(out))
	}
}

// TestKillDuringBaseline kills a watcher process after its first batch of
// a 2,000-file baseline commits; a second watcher finishes the baseline.
func TestKillDuringBaseline(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 2000; i++ {
		put(t, filepath.Join(e.root, fmt.Sprintf("f%04d", i)), "x\n", 0o644)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWatcher$")
	cmd.Env = append(os.Environ(), "SC_WATCH_HELPER=1", "SC_HELPER_HOME="+e.home,
		"SC_HELPER_ROOT="+e.root, "SC_HELPER_PASSWD="+e.cfg.PasswdPath)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(out)
	for lines.Scan() && lines.Text() != "batch" {
	}
	cmd.Process.Kill()
	cmd.Wait()

	e.start() // the lock does not block the restart
	if err := e.st.IntegrityCheck(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		e.want(filepath.Join(e.root, fmt.Sprintf("f%04d", i)), "file first seen")
	}
}

// TestHelperWatcher is not a test of its own: it is the watcher process
// TestKillDuringBaseline kills.
func TestHelperWatcher(t *testing.T) {
	if os.Getenv("SC_WATCH_HELPER") == "" {
		t.Skip("only run as a helper process")
	}
	root := os.Getenv("SC_HELPER_ROOT")
	var once atomic.Bool
	testHookRecorded = func(int) {
		if once.CompareAndSwap(false, true) {
			fmt.Println("batch")
		}
	}
	w, err := New(Config{Home: os.Getenv("SC_HELPER_HOME"), ScopeText: testScope(root),
		PasswdPath: os.Getenv("SC_HELPER_PASSWD"), Quiet: 50 * time.Millisecond, Log: os.Stderr})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	w.Run(context.Background())
	os.Exit(0)
}

//go:build linux

package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/scope"
	"smartconfig/internal/store"
)

// Config holds every path, duration and limit of a watcher, so tests can
// shrink them (plan 6.1).
type Config struct {
	Home         string        // $SC_HOME
	Roots        []string      // nil: the scope's roots plus login .ssh roots
	ScopeText    string        // "": the embedded default scope
	PasswdPath   string        // "/etc/passwd"
	Quiet, Cap   time.Duration // 500 ms, 5 s
	RescanEvery  time.Duration // 1 h
	RescanMinGap time.Duration // 10 s
	StoreBackoff time.Duration // 30 s
	FloorBackoff time.Duration // 60 s
	FloorBytes   uint64        // 256 MiB
	UserFileMax  int64         // 64 KiB
	UserFileGap  time.Duration // 60 s
	MaxDirty     int           // 10,000
	Batch        int           // 50
	Log          io.Writer     // stderr
}

// Defaults fills the zero fields of c with the production values.
func (c Config) Defaults() Config {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&c.Quiet, 500*time.Millisecond)
	set(&c.Cap, 5*time.Second)
	set(&c.RescanEvery, time.Hour)
	set(&c.RescanMinGap, 10*time.Second)
	set(&c.StoreBackoff, 30*time.Second)
	set(&c.FloorBackoff, 60*time.Second)
	set(&c.UserFileGap, 60*time.Second)
	if c.FloorBytes == 0 {
		c.FloorBytes = 256 << 20
	}
	if c.UserFileMax == 0 {
		c.UserFileMax = 64 << 10
	}
	if c.MaxDirty == 0 {
		c.MaxDirty = 10000
	}
	if c.Batch == 0 || c.Batch > store.MaxBatch {
		c.Batch = store.MaxBatch
	}
	if c.PasswdPath == "" {
		c.PasswdPath = "/etc/passwd"
	}
	if c.Log == nil {
		c.Log = os.Stderr
	}
	return c
}

// Log priorities (syslog levels), written as a "<N>" prefix; the CLI turns
// it into what journald or a terminal wants (plan 7.6).
const (
	prioErr     = 3
	prioWarning = 4
	prioNotice  = 5
	prioInfo    = 6
)

// Dirty reasons: what made a path due. Only an event explains a change;
// the others add an intent suffix.
const (
	reasonEvent   = "event"
	reasonStartup = "startup"
	reasonRescan  = "rescan"
)

// entry is one dirty path, or (prefix) every stored path below a
// directory that moved away.
type entry struct {
	first, due time.Time
	reason     string
	created    bool      // proof of absence (6.4)
	prefix     bool      // expand to the stored paths below (a dir moved away)
	self       bool      // read the path itself
	moved      int       // consecutive Moved results
	notBefore  time.Time // a backoff no new event may cut short
}

// watchRef is one watched directory.
type watchRef struct {
	in  *Inotify
	wd  int
	gen int
}

// Watcher is scd: two inotify instances, their readers, and one worker
// that records through the store.
type Watcher struct {
	cfg   Config
	scope *scope.Scope
	st    *store.Store
	lock  *os.File
	sys   *Inotify // the scope's roots (and --root dirs)
	home  *Inotify // login .ssh roots

	mu        sync.Mutex
	dirs      map[string]watchRef         // watched dir -> its watch
	wds       map[*Inotify]map[int]string // instance -> wd -> dir
	listings  map[string]map[string]bool  // dir -> names seen since its walk
	dirty     map[string]*entry
	gen       int
	rescanReq string // reason of a requested rescan, "" if none
	// rescanAfter delays a requested rescan: one asked for because a root
	// itself moved or vanished waits until things settle (Cap).
	rescanAfter time.Time
	lastScan    time.Time
	logged      map[string]bool      // one-time log lines
	roots       []string             // usable roots of the last rescan
	gone        []string             // login .ssh roots gone at the last rescan
	baseline    *baseline            // startup counts, nil once logged
	sshSet      map[string]bool      // login .ssh roots of the last rescan
	userRows    map[string]time.Time // last row of each user-owned home file
	lowSpace    bool                 // under the free-space floor
	nEvent      int                  // dirty entries marked by events (the MaxDirty bound)
	stopping    atomic.Bool          // Run is stopping: walks end early

	poke chan struct{}
}

type baseline struct {
	start                time.Time
	walked               bool
	first, changed, gone int
}

// Test hooks.
var (
	testHookProcessed    func(path string) // the worker handled path
	testHookBeforeRecord func()            // the worker is about to pop due paths
	testHookPanic        func()            // runs in the worker loop
	testHookPanicLocked  func()            // runs in the worker loop, mu held
	// testHookBeforeStoreRecord runs after a batch is read and before it
	// is recorded.
	testHookBeforeStoreRecord func()
	// testHookRootsListed runs in New between the roots' lstat and the
	// SC_HOME check.
	testHookRootsListed func()
)

// New prepares a watcher (plan 6.2): it finds the roots, refuses an
// SC_HOME inside a scope root before creating anything, creates SC_HOME, takes
// the single-instance lock, opens the store and the inotify instances.
func New(cfg Config) (*Watcher, error) {
	cfg = cfg.Defaults()
	text := cfg.ScopeText
	var sc *scope.Scope
	if text == "" {
		sc = scope.Default()
	} else {
		var err error
		if sc, err = scope.Parse(text); err != nil {
			return nil, err
		}
	}
	home, err := filepath.Abs(cfg.Home)
	if err != nil {
		return nil, fmt.Errorf("SC_HOME %s: %w", cfg.Home, err)
	}
	cfg.Home = filepath.Clean(home)
	w := &Watcher{
		cfg: cfg, dirs: map[string]watchRef{}, wds: map[*Inotify]map[int]string{},
		listings: map[string]map[string]bool{}, dirty: map[string]*entry{},
		logged: map[string]bool{}, poke: make(chan struct{}, 1),
		sshSet: map[string]bool{}, userRows: map[string]time.Time{},
	}
	roots, _, _, machine := w.usableRoots(sc)
	w.scope = machine
	if testHookRootsListed != nil {
		testHookRootsListed()
	}
	// A login .ssh is not checked here: its user can swap it for a symlink
	// after the lstat above, so the startup rescan skips it instead.
	if err := checkHome(cfg.Home, roots); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create SC_HOME %s: %w", cfg.Home, err)
	}
	if w.lock, err = lockHome(cfg.Home); err != nil {
		return nil, err
	}
	fail := func(err error) (*Watcher, error) { w.close(); return nil, err }
	if err := store.Init(cfg.Home); err != nil {
		return fail(err)
	}
	if w.st, err = store.Open(cfg.Home); err != nil {
		return fail(err)
	}
	w.st.SetFingerprintOnly(sc.FingerprintOnly)
	if w.sys, err = NewInotify(); err != nil {
		return fail(err)
	}
	if w.home, err = NewInotify(); err != nil {
		return fail(err)
	}
	w.wds[w.sys], w.wds[w.home] = map[int]string{}, map[int]string{}
	w.baseline = &baseline{start: time.Now()}
	return w, nil
}

// sshRoots returns <home>/.ssh for each login account in PasswdPath.
func (w *Watcher) sshRoots() []string {
	if w.cfg.Roots != nil {
		return nil
	}
	b, err := os.ReadFile(w.cfg.PasswdPath)
	if err != nil {
		w.logOnce("passwd", prioErr, fmt.Sprintf("cannot read %s: %v; no .ssh dirs watched", w.cfg.PasswdPath, err))
		return nil
	}
	var out []string
	for _, h := range scope.LoginHomes(b) {
		out = append(out, filepath.Join(h, ".ssh"))
	}
	return out
}

// usableRoots computes the roots (plan 6.2 step 1) and updates the scope
// with the login roots. A root is used only if lstat shows a real
// directory; a missing or symlinked one is skipped and logged once. The
// scope's roots (or cfg.Roots) and the login .ssh roots come back apart,
// and so do the login roots that are gone: missing, a symlink or not a
// directory. Their recorded files get deleted rows; a scope root that is
// gone gets none, since a separate /boot may only be unmounted.
func (w *Watcher) usableRoots(sc *scope.Scope) (fixed, login, gone []string, machine *scope.Scope) {
	ssh := w.sshRoots()
	candidates := w.cfg.Roots
	if candidates == nil {
		candidates = sc.Roots()
	}
	extra := append([]string(nil), ssh...)
	extra = append(extra, w.cfg.Roots...)
	machine = sc.With(w.cfg.Home, extra)
	usable := func(r string) (ok, isGone bool) {
		fi, err := os.Lstat(r)
		switch {
		case err != nil:
			w.logOnce("root "+r, prioInfo, fmt.Sprintf("not watching %s: %v", show(r), errText(err)))
			return false, fsutil.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR)
		case fi.Mode()&os.ModeSymlink != 0:
			w.logOnce("root "+r, prioWarning, fmt.Sprintf("not watching %s: it is a symlink", show(r)))
		case !fi.IsDir():
			w.logOnce("root "+r, prioWarning, fmt.Sprintf("not watching %s: not a directory", show(r)))
		default:
			return true, false
		}
		return false, true
	}
	for _, r := range candidates {
		r = filepath.Clean(r)
		if ok, _ := usable(r); ok {
			fixed = append(fixed, r)
		}
	}
	for _, r := range ssh {
		r = filepath.Clean(r)
		switch ok, isGone := usable(r); {
		case ok:
			login = append(login, r)
		case isGone:
			gone = append(gone, r)
		}
	}
	return fixed, login, gone, machine
}

func errText(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// checkHome refuses an SC_HOME that equals or lies below a root (plan 6.2
// step 2). Both sides are resolved only for this comparison: SC_HOME
// through its longest existing ancestor (it may not exist yet), each root
// with EvalSymlinks.
func checkHome(home string, roots []string) error {
	resolved, err := resolveMissing(home)
	if err != nil {
		return err
	}
	for _, r := range roots {
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		if resolved == rr || strings.HasPrefix(resolved, strings.TrimSuffix(rr, "/")+"/") {
			return fmt.Errorf("SC_HOME %s is inside watched root %s", home, r)
		}
	}
	return nil
}

// resolveMissing resolves the longest existing ancestor of p and appends
// the missing rest. A dangling symlink on the way is refused.
func resolveMissing(p string) (string, error) {
	var rest []string
	cur := p
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		if cur == "/" {
			break
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = filepath.Dir(cur)
	}
	r, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", fmt.Errorf("SC_HOME %s: %s does not resolve: %v", p, cur, errText(err))
	}
	return filepath.Join(append([]string{r}, rest...)...), nil
}

// lockHome takes the single-instance lock $SC_HOME/scd.lock and writes
// this process's pid into it.
func lockHome(home string) (*os.File, error) {
	p := filepath.Join(home, "scd.lock")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %v", p, errText(err))
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b, _ := os.ReadFile(p)
		f.Close()
		pid := strings.TrimSpace(string(b))
		if pid == "" {
			pid = "?"
		}
		return nil, fmt.Errorf("scd already running on %s (pid %s)", home, pid)
	}
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, nil
}

// close releases what New acquired.
func (w *Watcher) close() {
	for _, in := range []*Inotify{w.sys, w.home} {
		if in != nil {
			in.Close()
		}
	}
	if w.st != nil {
		w.st.Close()
	}
	if w.lock != nil {
		w.lock.Close()
	}
}

// Run watches until ctx is cancelled (plan 6.2 steps 7-8, 6.3). It returns
// nil after a clean stop, or the error of a goroutine that panicked.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 3)
	var wg sync.WaitGroup
	start := func(name string, f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errc <- fmt.Errorf("internal error in the %s: %v", name, r)
					cancel()
					if os.Getenv("SC_DEBUG") != "" {
						debug.PrintStack()
					}
				}
			}()
			f()
		}()
	}
	w.mu.Lock()
	w.rescanReq = reasonStartup
	w.mu.Unlock()
	start("system reader", func() { w.read(ctx, w.sys) })
	start("home reader", func() { w.read(ctx, w.home) })
	start("worker", func() { w.work(ctx) })
	<-ctx.Done()
	w.stopping.Store(true)
	w.sys.Close()
	w.home.Close()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case err := <-errc:
		// A goroutine that panicked while holding mu may leave the others
		// blocked for good: give them a moment, then report anyway, so
		// the process exits and systemd restarts it.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return err
	case <-done:
	}
	w.logLine(prioInfo, "stopped")
	select {
	case err := <-errc:
		return err
	default:
		return nil
	}
}

// read handles one instance's events until it is closed (plan 6.3, 9-15).
func (w *Watcher) read(ctx context.Context, in *Inotify) {
	buf := make([]byte, ReadBuf)
	for {
		if testHookBeforeRead != nil {
			testHookBeforeRead(in)
		}
		evs, err := in.Read(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
				return
			}
			w.logOnce("read: "+err.Error(), prioErr, "inotify read: "+err.Error())
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, e := range evs {
			w.handle(in, e)
		}
		w.wake()
	}
}

// handle applies one event to the maps and the dirty set.
func (w *Watcher) handle(in *Inotify, e Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e.Wd == -1 && e.Mask&syscall.IN_Q_OVERFLOW != 0 {
		which := "system"
		if in == w.home {
			which = "home"
		}
		w.logLine(prioErr, "event queue of the "+which+" roots overflowed; rescanning them")
		for d, ref := range w.dirs {
			if ref.in == in {
				delete(w.listings, d)
			}
		}
		w.requestRescanLocked(reasonRescan)
		return
	}
	dir, ok := w.wds[in][e.Wd]
	if !ok {
		return
	}
	if e.Mask&syscall.IN_IGNORED != 0 {
		delete(w.wds[in], e.Wd)
		if ref, ok := w.dirs[dir]; ok && ref.in == in && ref.wd == e.Wd {
			delete(w.dirs, dir)
			delete(w.listings, dir)
		}
		if w.isRoot(dir) {
			w.rootChangedLocked()
		}
		return
	}
	if e.Mask&(syscall.IN_MOVE_SELF|syscall.IN_DELETE_SELF) != 0 {
		if w.isRoot(dir) {
			w.rootChangedLocked()
		}
		return
	}
	if e.Name == "" {
		return // the directory itself (directory metadata is M3)
	}
	p := dir + "/" + e.Name
	if !w.scope.Recorded(p) {
		return
	}
	isDir := e.Mask&syscall.IN_ISDIR != 0
	arrived := e.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0
	// Proof of absence (6.4): the parent's listing is complete and the
	// name was never in it since the walk.
	created := false
	if list, ok := w.listings[dir]; ok && arrived {
		created = !list[e.Name]
	}
	if l, ok := w.listings[dir]; ok && arrived {
		l[e.Name] = true
	}
	switch {
	case isDir && arrived:
		w.mu.Unlock()
		w.walk(in, p, reasonEvent, created, w.currentGen())
		w.mu.Lock()
	case isDir && e.Mask&syscall.IN_MOVED_FROM != 0:
		w.unwatchBelow(p)
		w.markLocked(p, reasonEvent, false, true)
		// After renameat2(RENAME_EXCHANGE) another directory is at p, and
		// the unwatch above may have removed the watches its arrival under
		// p just added: walk it again.
		if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
			w.mu.Unlock()
			w.walk(in, p, reasonEvent, false, w.currentGen())
			w.mu.Lock()
		}
	case isDir:
		// DELETE of a directory: its children's events cover it.
	default:
		w.markLocked(p, reasonEvent, created, false)
	}
}

func (w *Watcher) currentGen() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gen
}

// rootOfLocked returns the root d.path lies under (the longest), or ""
// (caller holds mu). A login root that is gone counts: reading below it
// finds the file absent, or refuses the symlink that took its place.
func (w *Watcher) rootOfLocked(p string) string {
	best := ""
	for _, rs := range [][]string{w.roots, w.gone} {
		for _, r := range rs {
			if strings.HasPrefix(p, r+"/") && len(r) > len(best) {
				best = r
			}
		}
	}
	return best
}

// isRoot reports whether dir is one of the roots (caller holds mu).
func (w *Watcher) isRoot(dir string) bool {
	for _, r := range w.roots {
		if r == dir {
			return true
		}
	}
	return false
}

// unwatchBelow removes the watches at or below dir (caller holds mu).
func (w *Watcher) unwatchBelow(dir string) {
	for d, ref := range w.dirs {
		if d == dir || strings.HasPrefix(d, dir+"/") {
			ref.in.RmWatch(ref.wd)
			delete(w.wds[ref.in], ref.wd)
			delete(w.dirs, d)
			delete(w.listings, d)
		}
	}
}

// markLocked makes p dirty (caller holds mu): a new entry is due after the
// quiet period, an existing one is pushed back, but never past first+Cap.
func (w *Watcher) markLocked(p, reason string, created, prefix bool) {
	now := time.Now()
	e := w.dirty[p]
	if e == nil && reason == reasonEvent && w.overDirtyLocked() {
		return
	}
	if e == nil {
		e = &entry{first: now, reason: reason}
		w.dirty[p] = e
		if reason == reasonEvent {
			w.nEvent++
		}
	} else if reason == reasonEvent && e.reason != reasonEvent {
		w.nEvent++
	}
	if prefix {
		e.prefix = true
	} else {
		e.self = true // also when a moved-away dir's name is reused at once
	}
	if reason == reasonEvent {
		e.reason = reasonEvent // a live event explains the change
	}
	e.created = e.created || created
	due := now.Add(w.cfg.Quiet)
	if reason != reasonEvent {
		due = now
	}
	if limit := e.first.Add(w.cfg.Cap); due.After(limit) {
		due = limit
	}
	if due.Before(e.notBefore) {
		due = e.notBefore
	}
	e.due = due
}

func (w *Watcher) mark(p, reason string, created bool) {
	w.mu.Lock()
	w.markLocked(p, reason, created, false)
	w.mu.Unlock()
}

func (w *Watcher) wake() {
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

func (w *Watcher) requestRescanLocked(reason string) {
	if w.rescanReq == "" || w.rescanReq == reasonRescan && reason == reasonStartup {
		w.rescanReq = reason
	}
}

// rootChangedLocked asks for a rescan once a moved or removed root has
// had Cap to settle: a root moved away and back (or a ~/.ssh replaced by
// a new one) is then found in place. Only the first change of a burst
// sets the delay: a root that keeps moving (a user renaming ~/.ssh in a
// loop) must not hold back every rescan (caller holds mu).
func (w *Watcher) rootChangedLocked() {
	w.requestRescanLocked(reasonRescan)
	if now := time.Now(); !now.Before(w.rescanAfter) {
		w.rescanAfter = now.Add(w.cfg.Cap)
	}
}

// RequestRescan asks the worker for a rescan (sc watch on SIGHUP).
func (w *Watcher) RequestRescan() { w.requestRescan() }

// requestRescan asks the worker for a rescan.
func (w *Watcher) requestRescan() {
	w.mu.Lock()
	w.requestRescanLocked(reasonRescan)
	w.mu.Unlock()
	w.wake()
}

// walk watches dir and every in-scope directory below it, adding each
// watch before listing the directory, stores each listing and marks every
// recorded file and link. created holds for paths in a directory that
// itself has proof of absence.
func (w *Watcher) walk(in *Inotify, dir, reason string, created bool, gen int) int {
	if w.stopping.Load() {
		return 0
	}
	w.mu.Lock()
	var wd int
	var err error
	if testHookAddWatch != nil {
		err = testHookAddWatch(dir)
	}
	if err == nil {
		wd, err = in.AddWatch(dir)
	}
	switch {
	case errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR):
		w.mu.Unlock()
		return 0
	case err != nil:
		// Out of watches (ENOSPC) or another error: log once per rescan,
		// and still walk the directory, so every rescan reads it.
		w.mu.Unlock()
		w.logOnce(fmt.Sprintf("watch %d %s", gen, err), prioErr,
			fmt.Sprintf("cannot watch %s: %v (rescans still read it)", show(dir), errText(err)))
		w.mu.Lock()
	default:
		if old, ok := w.wds[in][wd]; ok && old != dir {
			delete(w.dirs, old) // the same inode under a new name
			delete(w.listings, old)
		}
		if prev, ok := w.dirs[dir]; ok && (prev.in != in || prev.wd != wd) {
			// Another directory was watched under this name, and the event
			// that it moved away was lost (an overflow): drop its watch, or
			// its events would be filed under this name.
			prev.in.RmWatch(prev.wd)
			delete(w.wds[prev.in], prev.wd)
		}
		w.wds[in][wd] = dir
		// The current generation, not the one the walk started with: a
		// reader's walk overlapping a rescan must not leave a watch the
		// rescan's cleanup would remove.
		w.dirs[dir] = watchRef{in: in, wd: wd, gen: w.gen}
	}
	w.mu.Unlock()

	ents, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	names := map[string]bool{}
	var subdirs []string
	w.mu.Lock()
	for _, de := range ents {
		names[de.Name()] = true
		p := dir + "/" + de.Name()
		if !w.scope.Recorded(p) {
			continue
		}
		if de.IsDir() {
			subdirs = append(subdirs, p)
			continue
		}
		w.markLocked(p, reason, created, false)
	}
	if old, ok := w.listings[dir]; ok {
		// Keep the names an event added during the listing, and any the
		// listing missed that are there now; drop those that are gone
		// (6.4: every walk replaces its listing). A name that is there
		// must never be dropped: a rename over it would look like a create.
		for n := range old {
			if names[n] {
				continue
			}
			if _, err := os.Lstat(dir + "/" + n); !fsutil.IsNotExist(err) {
				names[n] = true
			}
		}
	}
	w.listings[dir] = names
	w.mu.Unlock()
	w.wake()
	n := 1
	for _, d := range subdirs {
		n += w.walk(in, d, reason, created, gen)
	}
	return n
}

// rescan walks every usable root and marks every stored live path (plan
// 6.3 step 17), so changes no event reported are found.
func (w *Watcher) rescan(reason string) {
	if testHookRescan != nil {
		testHookRescan(reason)
	}
	base, err := w.baseScope()
	if err != nil {
		w.logOnce("scope", prioErr, err.Error())
		return
	}
	fixed, login, gone, machine := w.usableRoots(base)
	roots := append(fixed, login...)
	var keep []string
	for _, r := range roots {
		if err := checkHome(w.cfg.Home, []string{r}); err != nil {
			w.logOnce("home in "+r, prioErr, err.Error()+"; not watching it")
			continue
		}
		keep = append(keep, r)
	}
	ssh := map[string]bool{}
	for _, r := range w.sshRoots() {
		ssh[filepath.Clean(r)] = true
	}
	w.mu.Lock()
	w.scope = machine // login homes may have changed
	w.sshSet = ssh
	w.gen++
	gen := w.gen
	w.roots = keep
	w.gone = gone
	w.lastScan = time.Now()
	w.mu.Unlock()
	dirs := 0
	for _, r := range keep {
		in := w.sys
		if ssh[r] {
			in = w.home
		}
		dirs += w.walk(in, r, reason, false, gen)
	}
	live, err := w.st.LivePaths()
	if err != nil {
		w.logOnce("live: "+err.Error(), prioErr, err.Error())
	}
	w.mu.Lock()
	for _, p := range live {
		if w.scope.Recorded(p) && (underAny(p, keep) || underAny(p, gone)) {
			w.markLocked(p, reason, false, false)
		}
	}
	// Watches from older walks that this walk did not reach are gone.
	for d, ref := range w.dirs {
		if ref.gen < gen {
			ref.in.RmWatch(ref.wd)
			delete(w.wds[ref.in], ref.wd)
			delete(w.dirs, d)
			delete(w.listings, d)
		}
	}
	if b := w.baseline; b != nil && reason == reasonStartup && !b.walked {
		b.walked = true
		w.logLine(prioInfo, fmt.Sprintf("watching %s (%d directories)", describeRoots(keep, ssh), dirs))
	}
	w.mu.Unlock()
}

// baseScope is the scope without this machine's roots and SC_HOME.
func (w *Watcher) baseScope() (*scope.Scope, error) {
	if w.cfg.ScopeText == "" {
		return scope.Default(), nil
	}
	return scope.Parse(w.cfg.ScopeText)
}

func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

func describeRoots(roots []string, ssh map[string]bool) string {
	var parts []string
	nssh := 0
	for _, r := range roots {
		if ssh[r] {
			nssh++
			continue
		}
		parts = append(parts, show(r))
	}
	switch nssh {
	case 0:
	case 1:
		parts = append(parts, "1 .ssh dir")
	default:
		parts = append(parts, fmt.Sprintf("%d .ssh dirs", nssh))
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// work is the worker loop (plan 6.3 steps 16-19).
func (w *Watcher) work(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for ctx.Err() == nil {
		if testHookPanic != nil {
			testHookPanic()
		}
		w.mu.Lock()
		if testHookPanicLocked != nil {
			testHookPanicLocked()
		}
		w.periodicLocked(time.Now())
		reason := w.rescanReq
		settled := !time.Now().Before(w.rescanAfter)
		if reason != "" && (reason == reasonStartup || settled && time.Since(w.lastScan) >= w.cfg.RescanMinGap) {
			w.rescanReq = ""
			w.mu.Unlock()
			w.rescan(reason)
			continue
		}
		if testHookBeforeRecord != nil {
			w.mu.Unlock()
			testHookBeforeRecord()
			w.mu.Lock()
		}
		batch, next := w.popDue(time.Now())
		lastScan, rescanAfter := w.lastScan, w.rescanAfter // read under mu
		w.mu.Unlock()
		if len(batch) > 0 {
			w.process(ctx, batch)
			continue
		}
		w.maybeLogBaseline()
		wait := time.Until(next)
		if next.IsZero() {
			wait = w.cfg.RescanEvery
		}
		if reason != "" {
			gap := w.cfg.RescanMinGap - time.Since(lastScan)
			if s := time.Until(rescanAfter); s > gap {
				gap = s
			}
			if gap < wait {
				wait = gap
			}
		}
		if tick := w.cfg.RescanEvery - time.Since(lastScan); tick < wait {
			wait = tick
		}
		if wait < 0 {
			wait = 0
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-w.poke:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

type due struct {
	path string
	e    entry
}

// popDue removes up to Batch due entries, oldest due first, and returns
// them with the earliest due time of the rest (caller holds mu).
func (w *Watcher) popDue(now time.Time) ([]due, time.Time) {
	var all []due
	var next time.Time
	for p, e := range w.dirty {
		if !e.due.After(now) {
			all = append(all, due{p, *e})
		} else if next.IsZero() || e.due.Before(next) {
			next = e.due
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].e.due.Before(all[j].e.due) })
	if len(all) > w.cfg.Batch {
		if next.IsZero() || all[w.cfg.Batch].e.due.Before(next) {
			next = all[w.cfg.Batch].e.due
		}
		all = all[:w.cfg.Batch]
	}
	for _, d := range all {
		delete(w.dirty, d.path)
		if d.e.reason == reasonEvent {
			w.nEvent--
		}
	}
	return all, next
}

// process reads and records one batch outside mu.
func (w *Watcher) process(ctx context.Context, batch []due) {
	var obs []store.Obs
	var handled []due
	for _, d := range batch {
		if d.e.prefix {
			w.expandPrefix(d.path)
			d.e.prefix = false
			if !d.e.self {
				continue
			}
		}
		o, ok, requeued := w.observe(d)
		if !ok {
			if !requeued {
				w.processed(d.path)
			}
			continue
		}
		obs = append(obs, o)
		handled = append(handled, d)
	}
	if len(obs) == 0 {
		return
	}
	if w.underFloor() {
		needs := make([]bool, len(obs)) // hashing and stats outside mu
		for i, o := range obs {
			needs[i] = w.needsObject(o)
		}
		keepObs, keepDue := obs[:0], handled[:0]
		w.mu.Lock()
		for i, o := range obs {
			if needs[i] {
				w.holdLocked(handled[i], time.Now().Add(w.cfg.FloorBackoff))
				continue
			}
			keepObs, keepDue = append(keepObs, o), append(keepDue, handled[i])
		}
		w.mu.Unlock()
		obs, handled = keepObs, keepDue
		if len(obs) == 0 {
			return
		}
	}
	if testHookBeforeStoreRecord != nil {
		testHookBeforeStoreRecord()
	}
	res, err := w.st.Record(obs)
	if err != nil {
		w.logOnce("store: "+err.Error(), prioErr, "store: "+err.Error())
		w.mu.Lock()
		for _, d := range handled {
			w.holdLocked(d, time.Now().Add(w.cfg.StoreBackoff))
		}
		w.mu.Unlock()
		return
	}
	if testHookRecorded != nil {
		testHookRecorded(len(res))
	}
	for i, r := range res {
		d := handled[i]
		switch {
		case r.Moved:
			w.mu.Lock()
			d.e.moved++
			w.remarkLocked(d, time.Now().Add(w.cfg.Quiet))
			w.mu.Unlock()
		case r.Recorded:
			w.noteUserRow(r.Change)
			w.logRow(r.Change, d.e.reason)
			if d.path == w.cfg.PasswdPath {
				w.requestRescan()
			}
		}
		if !r.Moved {
			w.processed(d.path)
		}
	}
}

func (w *Watcher) processed(p string) {
	if testHookProcessed != nil {
		testHookProcessed(p)
	}
}

// observe reads one due path into an observation (plan 6.3 step 18). ok
// is false when there is nothing to record now; requeued then says the
// path was put back to be read again.
func (w *Watcher) observe(d due) (o store.Obs, ok, requeued bool) {
	o = store.Obs{Path: d.path, Created: d.e.created, CheckStamp: true, Origin: store.OriginAuto}
	switch d.e.reason {
	case reasonStartup:
		o.Suffix = store.SuffixNotWatching
	case reasonRescan:
		o.Suffix = store.SuffixRescan
	}
	// Read through the root with O_NOFOLLOW at every level: a user who
	// controls a root (a login's ~/.ssh) cannot point a directory in it at
	// files root may read (chunk D review).
	w.mu.Lock()
	root := w.rootOfLocked(d.path)
	w.mu.Unlock()
	if root == "" {
		return o, false, false
	}
	st, err := fsutil.ReadStateBelow(root, d.path)
	switch {
	case fsutil.IsNotExist(err):
		// Absent. After 10 moved results in a row, record it without the
		// stamp check, as for a file that keeps changing.
		if d.e.moved >= 10 {
			o.CheckStamp = false
			o.Suffix += store.SuffixChanging
		}
		return o, true, false
	case errors.Is(err, fsutil.ErrUnsafePath):
		// A directory on the way became a symlink: nothing is read through
		// it, and the recorded file is gone from where it was.
		w.logOnce("unsafe "+d.path, prioWarning, "not reading "+show(d.path)+": a directory on the way is a symlink")
		o.CheckStamp = false
		return o, true, false
	case errors.Is(err, fsutil.ErrTooBig):
		w.logOnce("big "+d.path, prioWarning, err.Error())
		return o, false, false
	case errors.Is(err, fsutil.ErrNotRecordable):
		// A directory, FIFO, socket or device took the path: the file or
		// link recorded there is gone (no row if none was recorded). The
		// stamp check would only see the newcomer, so it is skipped.
		o.CheckStamp = false
		return o, true, false
	case errors.Is(err, fsutil.ErrReplaced):
		w.mu.Lock()
		w.remarkLocked(d, time.Now().Add(w.cfg.Quiet))
		w.mu.Unlock()
		return o, false, true
	case err != nil:
		w.logOnce("read "+d.path, prioErr, err.Error())
		return o, false, false
	}
	stillChanging := !st.Stable || d.e.moved >= 10
	if stillChanging && time.Now().Before(d.e.first.Add(w.cfg.Cap)) && d.e.moved < 10 {
		w.mu.Lock()
		w.remarkLocked(d, time.Now().Add(w.cfg.Quiet))
		w.mu.Unlock()
		return o, false, true
	}
	if stillChanging {
		o.Suffix += store.SuffixChanging
		o.CheckStamp = false
	}
	o.State = &st
	if until := w.limitUserFile(&o); !until.IsZero() {
		w.mu.Lock()
		w.holdLocked(d, until)
		w.mu.Unlock()
		return o, false, true
	}
	return o, true, false
}

// remarkLocked puts a popped entry back, due at t, keeping its first time,
// reason and flags; a newer entry for the path is merged (caller holds mu).
func (w *Watcher) remarkLocked(d due, t time.Time) {
	e := d.e
	e.due = t
	if cur := w.dirty[d.path]; cur != nil {
		cur.created = cur.created || e.created
		cur.self = cur.self || e.self
		if t.Before(cur.due) && !t.Before(cur.notBefore) {
			cur.due = t
		}
		cur.moved = e.moved
		return
	}
	w.dirty[d.path] = &e
	if e.reason == reasonEvent {
		w.nEvent++
	}
}

// holdLocked is remarkLocked for a backoff (store error, free-space floor,
// user-file gap): no event marks the path due before t (caller holds mu).
func (w *Watcher) holdLocked(d due, t time.Time) {
	d.e.notBefore = t
	w.remarkLocked(d, t)
	if cur := w.dirty[d.path]; cur != nil {
		cur.notBefore = t
		if cur.due.Before(t) {
			cur.due = t
		}
	}
}

// expandPrefix marks every live stored path below dir: it moved away, so
// they are now absent and get deleted rows.
func (w *Watcher) expandPrefix(dir string) {
	live, err := w.st.LivePaths()
	if err != nil {
		w.logOnce("live: "+err.Error(), prioErr, err.Error())
		return
	}
	w.mu.Lock()
	for _, p := range live {
		if strings.HasPrefix(p, dir+"/") && w.scope.Recorded(p) {
			w.markLocked(p, reasonEvent, false, false)
			if e := w.dirty[p]; e != nil { // nil after the dirty-set bound
				e.due = time.Now()
			}
		}
	}
	w.mu.Unlock()
	w.wake()
}

// maybeLogBaseline logs the startup summary once the startup rescan's
// paths are all handled.
func (w *Watcher) maybeLogBaseline() {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.baseline
	if b == nil || !b.walked {
		return
	}
	for _, e := range w.dirty {
		if e.reason == reasonStartup {
			return
		}
	}
	w.baseline = nil
	w.logLine(prioInfo, fmt.Sprintf("baseline: %d first seen, %d changed and %d deleted while not watching (%s)",
		b.first, b.changed, b.gone, time.Since(b.start).Round(time.Millisecond)))
}

// logRow writes one line per recorded row (plan 7.6), path first; first
// seen rows of the startup baseline are only counted.
func (w *Watcher) logRow(c store.Change, reason string) {
	w.mu.Lock()
	if b := w.baseline; b != nil && reason == reasonStartup {
		switch {
		case strings.HasPrefix(c.Intent, "first seen"):
			b.first++
			w.mu.Unlock()
			return
		case c.Kind == store.KindDeleted:
			b.gone++
		default:
			b.changed++
		}
	}
	tier := w.scope.Tier(c.Path)
	w.mu.Unlock()
	prio := prioInfo
	switch {
	case tier <= 2:
		prio = prioWarning
	case tier == 3:
		prio = prioNotice
	}
	intent := c.Intent // spaces are normal here; a link target may hold anything
	if !utf8.ValidString(intent) || strings.ContainsFunc(intent, unicode.IsControl) {
		intent = strconv.Quote(intent)
	}
	w.logLine(prio, fmt.Sprintf("T%d %s: %s (%s)", tier, quoteIfOdd(c.Path), intent, c.ID))
}

var logMu sync.Mutex

func (w *Watcher) logLine(prio int, line string) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(w.cfg.Log, "<%d>%s\n", prio, strings.ReplaceAll(line, "\n", " "))
}

// maxLogged bounds the memory of logOnce: keys can hold user-chosen paths.
const maxLogged = 10000

// logOnce logs line only the first time key is seen (the memory is reset
// when it grows past maxLogged, so a flood of names cannot grow it).
func (w *Watcher) logOnce(key string, prio int, line string) {
	logMu.Lock()
	if len(w.logged) >= maxLogged {
		w.logged = map[string]bool{}
	}
	seen := w.logged[key]
	w.logged[key] = true
	logMu.Unlock()
	if !seen {
		w.logLine(prio, line)
	}
}

// quoteIfOdd quotes s with %q when it holds a space, a quote, a
// backslash, a control character or invalid UTF-8 (plan 7.2).
func quoteIfOdd(s string) string {
	if !utf8.ValidString(s) || strings.ContainsAny(s, " \"\\") || strings.ContainsFunc(s, unicode.IsControl) {
		return strconv.Quote(s)
	}
	return s
}

// show is quoteIfOdd for paths in messages.
func show(s string) string { return quoteIfOdd(s) }

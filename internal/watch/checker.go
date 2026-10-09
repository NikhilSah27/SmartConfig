//go:build linux

package watch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"smartconfig/internal/check"
	"smartconfig/internal/store"
)

// A check after a recorded change (M3 plan 7): the worker hands the path
// of an automatic file row to one checker goroutine through a bounded set
// and never waits for it. The checker compares the new content with the
// content before the change and logs only what the change added.

// checkJob is a path waiting to be checked: the first row of the changes
// queued for it (the content before is the row before that one) and the
// newest.
type checkJob struct {
	first, last string // row ids
	deliberate  bool   // the job holds only a restore or sc edit row
}

// checkQueue is the bounded set of paths waiting for the checker.
type checkQueue struct {
	mu    sync.Mutex
	jobs  map[string]*checkJob
	order []string
	wake  chan struct{}
	max   int
}

func newCheckQueue(max int) *checkQueue {
	return &checkQueue{jobs: map[string]*checkJob{}, wake: make(chan struct{}, 1), max: max}
}

// add queues row for its path. A path already waiting keeps its first row,
// so the check compares with the content before all of its changes. It
// reports false when the queue is full and the path is not in it.
func (q *checkQueue) add(c store.Change) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	deliberate := c.Origin != store.OriginAuto
	if j := q.jobs[c.Path]; j != nil {
		if j.deliberate && !deliberate {
			j.first, j.deliberate = c.ID, false // compare with the restored content
		}
		j.last = c.ID
		return true
	}
	if len(q.order) >= q.max {
		return false
	}
	q.jobs[c.Path] = &checkJob{first: c.ID, last: c.ID, deliberate: deliberate}
	q.order = append(q.order, c.Path)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

// next takes the oldest waiting path, or reports false.
func (q *checkQueue) next() (string, checkJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return "", checkJob{}, false
	}
	p := q.order[0]
	q.order = q.order[1:]
	j := *q.jobs[p]
	delete(q.jobs, p)
	return p, j, true
}

// queueCheck hands a recorded row to the checker when the graph has a
// checker for its path. Not checked: rows with no content (deleted, link,
// digest), and the startup baseline's first-seen rows, which are no
// change. A full queue drops the path and says so once until it drains:
// sc check still finds the problem.
func (w *Watcher) queueCheck(c store.Change, reason string) {
	deliberate := c.Origin == store.OriginRestore || c.Origin == store.OriginEdit
	if deliberate {
		if _, ok := w.lastFound(c.Path, c.Blob); ok || reason == reasonStartup {
			return
		}
	}
	if w.checks == nil || c.Kind != store.KindFile || !(c.Origin == store.OriginAuto || deliberate) ||
		(reason == reasonStartup && strings.HasPrefix(c.Intent, "first seen")) ||
		w.checkGraph.Checker(c.Path) == "" {
		return
	}
	if !w.checkQ.add(c) {
		logMu.Lock()
		w.dropped++
		logMu.Unlock()
		w.logOnce("check queue full", prioWarning, fmt.Sprintf("check queue full: %s was not checked (sc check %s)", show(c.Path), show(c.Path)))
		return
	}
	logMu.Lock()
	delete(w.logged, "check queue full") // drained enough to take one: the next drop is news
	n := w.dropped
	w.dropped = 0
	logMu.Unlock()
	if n > 1 {
		// The first line named one path: say how many went unchecked.
		w.logLine(prioWarning, fmt.Sprintf("check queue full: %d changed files were not checked in all (sc check)", n))
	}
}

// checker checks queued paths until ctx is cancelled.
func (w *Watcher) checker(ctx context.Context) {
	for {
		p, j, ok := w.checkQ.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-w.checkQ.wake:
			}
			continue
		}
		if testHookBeforeCheck != nil {
			testHookBeforeCheck(p)
		}
		w.checkPath(ctx, p, j)
		if testHookChecked != nil {
			testHookChecked(p)
		}
	}
}

// Test hooks for the checker.
var (
	testHookBeforeCheck func(path string) // the checker is about to check path
	testHookChecked     func(path string) // the checker is done with path
)

// checkPath checks the newest content of p against the content before the
// first queued change, logs the findings the change added, and says so
// when a file it reported is clean of them again.
func (w *Watcher) checkPath(ctx context.Context, p string, j checkJob) {
	// The two rows it needs, not the path's whole history (M3 follow-up
	// 7: that took 0.1 s at 20,000 rows).
	last, before, err := w.st.Around(p, j.first, j.last)
	if err != nil {
		w.logOnce("check list "+p, prioErr, fmt.Sprintf("check: %s: %v", show(p), err))
		return
	}
	if last == nil {
		return // gone from the store: nothing to say
	}
	after, err := w.st.Blob(last.Blob)
	if err != nil {
		w.logOnce("check blob "+p, prioErr, fmt.Sprintf("check: %s: %v", show(p), err))
		return
	}
	// The content before: what the last check of p found, when that was
	// this version, else a check of it now. If that fails, every finding of
	// the new content counts as new: better a known problem said again
	// than a new one not said.
	var had []check.Finding
	stale := false
	if before != nil && before.Origin != store.OriginAuto {
		_, ok := w.lastFound(p, before.Blob)
		stale = !ok // nothing checked it: what was reported is not what was before
	}
	if before != nil && before.Kind == store.KindFile {
		if c, ok := w.lastFound(p, before.Blob); ok {
			had = c
		} else if data, err := w.st.Blob(before.Blob); err == nil {
			// Judged by its own mode and owner: the file's on disk now
			// are the new version's (M5 follow-up 5a).
			rep, err := w.checks.CheckVersion(ctx, p, data, check.Meta{Mode: before.Mode, UID: before.UID, GID: before.GID})
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				w.logOnce("check before "+p, prioNotice, fmt.Sprintf("check: %s: the version before could not be checked (%v); all findings are reported", show(p), err))
			default:
				had = rep.Findings
				if rep.Incomplete {
					w.logOnce("check before "+p, prioNotice, fmt.Sprintf("check: %s: the version before was not fully checked (a validator did not finish); its problems may be reported as new", show(p)))
				}
			}
		}
	}
	rep, err := w.checks.Check(ctx, p, after)
	if err != nil {
		if ctx.Err() == nil {
			w.logOnce("check "+p, prioErr, fmt.Sprintf("check: %s: %v", show(p), err))
		}
		return
	}
	if rep.Unchecked {
		return // nothing was checked (a drop-in sc does not judge): nothing to say
	}
	if rep.Incomplete {
		// Not remembered, and no "ok again": a validator that did not
		// finish cannot say a problem it found before is gone.
		w.logOnce("check incomplete "+p, prioNotice, fmt.Sprintf("check: %s: a validator did not finish; the check is incomplete (sc check %s)", show(p), show(p)))
	} else {
		w.rememberFound(p, last.Blob, rep.Findings)
	}
	tier := w.tierOf(p)
	// A file reported before: clean of what was reported, it is ok again.
	w.mu.Lock()
	reported := w.failing[p]
	w.mu.Unlock()
	if stale {
		reported = nil
	}
	fixed := check.Added(rep.Findings, reported) // reported, and not there now
	if rep.Incomplete {
		fixed = nil
	}
	still := check.Added(fixed, reported)
	if len(reported) > 0 && len(still) == 0 {
		w.logLine(prioInfo, fmt.Sprintf("T%d %s: check: ok again (%s)", tier, show(p), last.ID))
	}
	added := check.Added(had, rep.Findings)
	if last.Origin != store.OriginAuto {
		added = nil // a restore or sc edit is deliberate (plan 7): only "ok again"
	}
	w.mu.Lock()
	if all := append(still, added...); len(all) > 0 {
		w.failing[p] = all
	} else {
		delete(w.failing, p)
	}
	if len(w.failing) > maxLogged {
		w.failing = map[string][]check.Finding{} // a flood of names cannot grow it
	}
	w.mu.Unlock()
	// One line per finding, at most maxCheckLines of them: the rest is one
	// line more. The journal gets the rule, the line and sc's sentence,
	// never the validator's own lines (they may quote the file).
	for i, f := range added {
		if i == maxCheckLines {
			w.logLine(checkPrio(check.Worst(added[i:])), fmt.Sprintf("T%d %s: check: %d more (sc check %s)", tier, show(p), len(added)-i, show(p)))
			break
		}
		where := ""
		if f.Line > 0 {
			where = fmt.Sprintf(", line %d", f.Line)
		}
		w.logLine(checkPrio(f.Severity), fmt.Sprintf("T%d %s: check: %s %s%s: %s (%s)", tier, show(p), f.Severity, f.Rule, where, oneLine(f.Text), last.ID))
	}
}

// oneLine returns a finding's sentence as it is, or quoted when it holds
// a control character or invalid UTF-8 (it may name a device or a path
// from the file).
func oneLine(s string) string {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return strconv.Quote(s)
	}
	return s
}

// found is what the last check of a path found in one version of it.
type found struct {
	blob     string
	findings []check.Finding
}

// lastFound returns what the last check of p found, if it checked the
// version with blob: the next change compares with it without running the
// validator on it again.
func (w *Watcher) lastFound(p, blob string) ([]check.Finding, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, ok := w.lastChecked[p]
	return f.findings, ok && f.blob == blob
}

func (w *Watcher) rememberFound(p, blob string, fs []check.Finding) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lastChecked) >= maxLogged {
		w.lastChecked = map[string]found{} // a flood of names cannot grow it
	}
	w.lastChecked[p] = found{blob, fs}
}

// maxCheckLines bounds the journal lines of one check.
const maxCheckLines = 5

// checkPrio is the journald priority of a finding's line: a blocker at
// err, an error at warning, a warning at notice.
func checkPrio(s check.Severity) int {
	switch s {
	case check.Blocker:
		return prioErr
	case check.Error:
		return prioWarning
	}
	return prioNotice
}

func (w *Watcher) tierOf(p string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.scope.Tier(p)
}

//go:build linux

package watch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"syscall"
	"time"

	"smartconfig/internal/store"
)

// Limits and degraded modes of the worker (plan 6.3 step 18, question 9).

// Test hooks for step 13's cases.
var (
	testHookBeforeRead func(in *Inotify)      // a reader is about to read
	testHookRescan     func(reason string)    // a rescan starts
	testHookAddWatch   func(dir string) error // fails add_watch for dir
	testHookStatfs     func() (free uint64)   // fakes SC_HOME's free space
	testHookRecorded   func(rows int)         // a batch was committed
)

// periodic asks for a rescan when RescanEvery has passed since the last
// one (caller holds mu).
func (w *Watcher) periodicLocked(now time.Time) {
	if w.rescanReq == "" && !w.lastScan.IsZero() && now.Sub(w.lastScan) >= w.cfg.RescanEvery {
		w.rescanReq = reasonRescan
	}
}

// freeBytes is the space left for SC_HOME's objects.
func (w *Watcher) freeBytes() (uint64, error) {
	if testHookStatfs != nil {
		return testHookStatfs(), nil
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(w.cfg.Home, &fs); err != nil {
		return 0, err
	}
	return fs.Bavail * uint64(fs.Bsize), nil
}

// underFloor reports whether free space is below FloorBytes, and logs
// one line when that state is entered and one when it is left.
func (w *Watcher) underFloor() bool {
	free, err := w.freeBytes()
	low := err == nil && free < w.cfg.FloorBytes
	w.mu.Lock()
	changed := low != w.lowSpace
	w.lowSpace = low
	w.mu.Unlock()
	if changed && low {
		w.logLine(prioErr, fmt.Sprintf("only %d MB free under %s; new content waits", free>>20, w.cfg.Home))
	} else if changed {
		w.logLine(prioInfo, "free space is back; recording new content again")
	}
	return low
}

// needsObject reports whether recording o would store new content: a
// file that is not fingerprint-only and whose content is not stored yet.
// Links, deletions, digests and mode or owner changes go through.
func (w *Watcher) needsObject(o store.Obs) bool {
	if o.State == nil || o.State.Kind != "file" || o.Digest || w.st.FingerprintOnly(o.Path) {
		return false
	}
	sum := sha256.Sum256(o.State.Data)
	return !w.st.HasObject(hex.EncodeToString(sum[:]))
}

// homeFile reports whether p lies under a login .ssh root: such files are
// size- and rate-limited whoever owns them (a user can make root's reads
// land on files root owns, so ownership proves nothing).
func (w *Watcher) homeFile(p string, uid int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for r := range w.sshSet {
		if strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// limitUserFile applies question 9's limits to a file under a login home:
// over UserFileMax it becomes a digest row, and it is recorded at most
// once per UserFileGap. It returns the time to wait until, or zero.
func (w *Watcher) limitUserFile(o *store.Obs) time.Time {
	if o.State == nil || !w.homeFile(o.Path, o.State.Meta.UID) {
		return time.Time{}
	}
	if o.State.Kind == "file" && int64(len(o.State.Data)) > w.cfg.UserFileMax {
		o.Digest = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.userRows[o.Path]; ok && time.Since(last) < w.cfg.UserFileGap {
		return last.Add(w.cfg.UserFileGap)
	}
	return time.Time{}
}

// pathRate is a system file's row budget (follow-up 8): PathBurst rows to
// spend, one more earned every PathGap. A file a program rewrites without
// pause gets PathBurst rows, then one per PathGap, each the newest state.
type pathRate struct {
	tokens float64   // rows left
	at     time.Time // when tokens was brought up to date
	warned bool      // the "changes constantly" line was logged
}

// refillLocked brings r up to now (caller holds mu).
func (w *Watcher) refillLocked(r *pathRate, now time.Time) {
	r.tokens = min(float64(w.cfg.PathBurst), r.tokens+float64(now.Sub(r.at))/float64(w.cfg.PathGap))
	r.at = now
}

// limitPath returns when the next row of a system file may be written if
// its budget is spent, or zero. Home files have their own limit.
func (w *Watcher) limitPath(p string) time.Time {
	if w.homeFile(p, 0) {
		return time.Time{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.rates[p]
	if r == nil {
		return time.Time{}
	}
	now := time.Now()
	w.refillLocked(r, now)
	if r.tokens >= 1 {
		return time.Time{}
	}
	if !r.warned {
		r.warned = true
		w.logLine(prioWarning, fmt.Sprintf("%s changes constantly: recording it at most every %g min, the newest state",
			show(p), w.cfg.PathGap.Minutes()))
	}
	return now.Add(time.Duration((1 - r.tokens) * float64(w.cfg.PathGap)))
}

// notePathRow spends one row of a system file's budget.
func (w *Watcher) notePathRow(p string) {
	if w.homeFile(p, 0) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	r := w.rates[p]
	if r == nil {
		r = &pathRate{tokens: float64(w.cfg.PathBurst), at: now}
		w.rates[p] = r
	}
	w.refillLocked(r, now)
	r.tokens--
}

// pruneRatesLocked forgets the budgets that are full again, so the map
// holds only files that changed lately; the next flood warns again
// (caller holds mu).
func (w *Watcher) pruneRatesLocked(now time.Time) {
	for p, r := range w.rates {
		if w.refillLocked(r, now); r.tokens >= float64(w.cfg.PathBurst) {
			delete(w.rates, p)
		}
	}
}

// noteUserRow remembers when a user-owned home file was last recorded.
func (w *Watcher) noteUserRow(c store.Change) {
	if !w.homeFile(c.Path, c.UID) {
		return
	}
	w.mu.Lock()
	w.userRows[c.Path] = time.Now()
	w.mu.Unlock()
}

// overDirtyLocked handles the dirty-set bound: more than MaxDirty event
// marks clear the set and ask for one rescan, which finds them all again
// (caller holds mu). Only event marks count: a rescan marks every path of
// the scope, which may be more than MaxDirty on its own.
func (w *Watcher) overDirtyLocked() bool {
	if w.nEvent < w.cfg.MaxDirty {
		return false
	}
	w.dirty = map[string]*entry{}
	w.nEvent = 0
	w.requestRescanLocked(reasonRescan)
	w.logLine(prioErr, fmt.Sprintf("more than %d changes waiting; rescanning instead", w.cfg.MaxDirty))
	return true
}

//go:build linux

package watch

import (
	"os"
	"time"

	"smartconfig/internal/aptrun"
	"smartconfig/internal/fsutil"
)

// dpkgRun is the dpkg run a change was seen in (M5 follow-ups, step 8).
type dpkgRun struct {
	name  string    // as rows name it (aptrun.Mark.Name)
	begun time.Time // when the mark was written, to the file system's clock
}

// aptMark is the last mark scd read. scd reads the mark for every event,
// thousands in a large run, so an unchanged mark costs one lstat, and its
// apt is looked up in /proc at most every aptLiveEvery.
type aptMark struct {
	stamp   fsutil.Stamp
	run     *dpkgRun // nil: no mark, or one that does not parse
	m       aptrun.Mark
	checked time.Time // when m's apt was last looked up
	live    bool
}

// aptLiveEvery is how long a mark's apt is taken as alive once seen: apt
// killed before its Post-Invoke has its last 100 ms tagged.
const aptLiveEvery = 100 * time.Millisecond

// aptRunLocked returns the dpkg run going on now, or nil (caller holds
// mu). sc apt end removes the mark, so the lstat sees a run end at once.
func (w *Watcher) aptRunLocked() *dpkgRun {
	if w.cfg.AptRun == "" {
		return nil
	}
	st, err := fsutil.LstatStamp(w.cfg.AptRun)
	if err != nil || !st.Mode.IsRegular() {
		return nil
	}
	c := &w.apt
	if st != c.stamp {
		// Read after the lstat: what is read is at least as new as st, and
		// a mark replaced in between has another stamp next time.
		*c = aptMark{stamp: st}
		b, err := os.ReadFile(w.cfg.AptRun)
		if err != nil {
			return nil
		}
		m, err := aptrun.Parse(b)
		if err != nil {
			return nil
		}
		// The mark holds whole seconds; its file's mtime says when in that
		// second sc apt begin wrote it.
		begun := time.Unix(0, st.Mtime)
		if begun.Before(m.Begun) || !begun.Before(m.Begun.Add(time.Second)) {
			begun = m.Begun
		}
		c.m, c.run = m, &dpkgRun{name: m.Name(), begun: begun}
	}
	if c.run == nil {
		return nil
	}
	if now := time.Now(); now.Sub(c.checked) >= aptLiveEvery {
		c.live, c.checked = c.m.Live(), now
	}
	if !c.live {
		return nil
	}
	return c.run
}

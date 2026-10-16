// Package aptrun is the mark of a dpkg run (M5 follow-ups, step 8). apt's
// hook runs sc apt begin before dpkg and sc apt end after it; begin writes
// the mark, end removes it, and scd tags each change it sees while a run's
// apt is alive with origin apt and the run.
//
// The mark names the apt process by its pid and its start time (apt runs
// its hook lines from a fork of itself, which is gone before dpkg runs:
// Runner names the apt it was forked from), so a mark
// that outlived its run (apt killed before its Post-Invoke) is seen as
// stale and tags nothing, and a reused pid is not taken for it.
package aptrun

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// File is where sc apt begin puts the mark: /run is a tmpfs, so a reboot
// ends any run too.
const File = "/run/smartconfig/apt-run"

// procDir is /proc; the tests point it elsewhere.
var procDir = "/proc"

// Mark is one dpkg run.
type Mark struct {
	PID   int       // the apt (or other dpkg front end) that runs dpkg
	Start uint64    // its start time, field 22 of /proc/PID/stat
	Begun time.Time // when sc apt begin ran
}

// Encode is the mark as its file holds it: "pid start unix-seconds\n".
func (m Mark) Encode() []byte {
	return []byte(fmt.Sprintf("%d %d %d\n", m.PID, m.Start, m.Begun.Unix()))
}

// Parse reads a mark Encode wrote.
func Parse(b []byte) (Mark, error) {
	f := strings.Fields(string(b))
	if len(f) != 3 {
		return Mark{}, fmt.Errorf("apt run mark %q: want 3 fields", b)
	}
	pid, err1 := strconv.Atoi(f[0])
	start, err2 := strconv.ParseUint(f[1], 10, 64)
	sec, err3 := strconv.ParseInt(f[2], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil || pid <= 0 {
		return Mark{}, fmt.Errorf("apt run mark %q: %v", b, err)
	}
	return Mark{PID: pid, Start: start, Begun: time.Unix(sec, 0)}, nil
}

// Name is the run as scd's rows name it: when it began, in UTC.
func (m Mark) Name() string { return m.Begun.UTC().Format("2006-01-02 15:04:05 UTC") }

// Live reports whether the mark's apt is still running.
func (m Mark) Live() bool {
	_, _, start, err := Stat(m.PID)
	return err == nil && start == m.Start
}

// Read returns the run marked in file, if its apt is alive.
func Read(file string) (Mark, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return Mark{}, false
	}
	m, err := Parse(b)
	if err != nil || !m.Live() {
		return Mark{}, false
	}
	return m, true
}

// Stat returns a process's command name, its parent and its start time,
// from /proc/PID/stat. The name, in parentheses, may hold blanks and
// parentheses itself: the fields after it start after the last ")".
func Stat(pid int) (comm string, ppid int, start uint64, err error) {
	b, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", 0, 0, err
	}
	s := string(b)
	i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if i < 0 || j < i {
		return "", 0, 0, fmt.Errorf("/proc/%d/stat: no command name", pid)
	}
	f := strings.Fields(s[j+1:]) // f[0] is field 3, state
	if len(f) < 20 {
		return "", 0, 0, fmt.Errorf("/proc/%d/stat: %d fields", pid, len(f)+2)
	}
	ppid, err1 := strconv.Atoi(f[1])
	start, err2 := strconv.ParseUint(f[19], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return "", 0, 0, fmt.Errorf("/proc/%d/stat: %v", pid, err)
	}
	return s[i+1 : j], ppid, start, nil
}

// shells are the commands apt's hook may run sc through: apt runs each
// Pre-Invoke and Post-Invoke line with sh -c.
var shells = map[string]bool{"sh": true, "dash": true, "bash": true, "busybox": true}

// Runner returns the process that runs dpkg, given sc's parent: the first
// one up from parent that is not a shell, or, when that one's parent has
// its name, the parent. apt runs its hook lines (RunScripts) in a fork of
// itself that exits once they are done, before dpkg runs; the apt it was
// forked from lives through the run (the chunk L review, L1). Only one
// such step is taken, so a long-lived daemon above is never named.
func Runner(parent int) (pid int, start uint64, err error) {
	pid = parent
	for range 8 {
		comm, ppid, start, err := Stat(pid)
		if err != nil {
			return 0, 0, err
		}
		if !shells[comm] || ppid <= 1 {
			if pcomm, _, pstart, err := Stat(ppid); err == nil && ppid > 1 && pcomm == comm {
				return ppid, pstart, nil // the fork's apt
			}
			return pid, start, nil
		}
		pid = ppid
	}
	return 0, 0, errors.New("no apt above a chain of shells")
}

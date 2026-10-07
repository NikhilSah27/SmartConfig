// Package boot keeps a verdict for every boot of the machine (M4 plan
// 3.2): an append-only text file, $SC_HOME/boots, next to the store. The
// store's schema stays as it is, so the backup binaries kept for rescue
// keep reading the store.
//
// Each line is shorter than PIPE_BUF and written with O_APPEND:
//
//	<boot id> seen <unix time>
//	<boot id> ok|bad <unix time> <newest store row> <why>
//
// "seen" is written early in every boot that can write /var
// (sc-boot-seen.service), the verdict once multi-user.target is reached
// (sc-boot-ok.service). A boot with a "seen" line and no verdict never
// got there.
package boot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// FileName is the boots file's name in $SC_HOME.
const FileName = "boots"

const (
	maxSize   = 1 << 20 // above this the file keeps its last keepLines lines
	keepLines = 1000
)

// Boot is what the file says about one boot.
type Boot struct {
	ID      string    // /proc/sys/kernel/random/boot_id, as the kernel writes it
	Seen    time.Time // zero when there is no "seen" line
	Verdict string    // "ok", "bad", or "" when the boot got none
	At      time.Time // when the verdict was given
	RowID   int64     // the newest store row at the verdict; -1 when the store could not be read
	Why     string    // what the verdict rests on
}

// IDPath is where the kernel says which boot this is. Tests replace it.
var IDPath = "/proc/sys/kernel/random/boot_id"

// CurrentID returns this boot's id.
func CurrentID() (string, error) {
	b, err := os.ReadFile(IDPath)
	if err != nil {
		return "", fmt.Errorf("boot id: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" || strings.ContainsAny(id, " \t\n") {
		return "", fmt.Errorf("boot id: %q is not one", id)
	}
	return id, nil
}

// Seen appends "<id> seen <now>" to the boots file in home.
func Seen(home, id string, now time.Time) error {
	return appendLine(home, fmt.Sprintf("%s seen %d", id, now.Unix()))
}

// Record appends a verdict for boot id: rowid is the newest store row, why
// what it rests on (one line).
func Record(home, id, verdict string, now time.Time, rowid int64, why string) error {
	if verdict != "ok" && verdict != "bad" {
		return fmt.Errorf("boot verdict %q", verdict)
	}
	why = strings.Join(strings.Fields(why), " ")
	return appendLine(home, fmt.Sprintf("%s %s %d %d %s", id, verdict, now.Unix(), rowid, why))
}

// Judge gives a boot's verdict from systemd's answers once multi-user is
// reached: ok when local-fs.target is active and the boot is in neither
// emergency nor rescue mode. A mount that never happened leaves
// local-fs.target inactive, not failed, and nothing else catches it (the
// M4 lab). Failed units are counted, but decide nothing: a healthy
// Ubuntu desktop may have some (whoopsie's on this VM).
func Judge(localFS, emergency, rescue string, failed int) (verdict, why string) {
	why = fmt.Sprintf("local-fs=%s emergency=%s rescue=%s failed-units=%d", localFS, emergency, rescue, failed)
	if localFS == "active" && emergency == "inactive" && rescue == "inactive" {
		return "ok", why
	}
	return "bad", why
}

func path(home string) string { return filepath.Join(home, FileName) }

// appendLine appends one line under an exclusive lock on the file, after
// keeping only the last keepLines lines when the file has grown past
// maxSize (rewritten in place, so a writer waiting for the lock appends
// to the same file), and syncs it: a boot reset soon after must not lose
// its line. A line a crash left unfinished is ended first, with tornMark,
// so the new one is not glued to it and the torn one is never read: cut
// in its row, "bbbb ok 1700000000 12" for row 1234, it would read whole
// and pull the healthy boot's line back (M4 follow-up 9). Root writes the
// file at boot: a symlink there, or anything not a regular file, is
// refused, never followed.
func appendLine(home, line string) error {
	f, err := os.OpenFile(path(home), os.O_RDWR|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("boots: lock: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("boots: %s is not a regular file", f.Name())
	}
	if fi.Size() > maxSize {
		if err := trim(f); err != nil {
			return err
		}
	} else if fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			line = tornMark + "\n" + line
		}
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	return nil
}

// tornMark ends a line a crash cut short; Read skips such a line.
const tornMark = " #torn"

// trim rewrites f, which is locked, with its last keepLines lines; a last
// line a crash left unfinished gets tornMark, as appendLine gives it (the
// chunk G review: trim used to end it plainly, and it read whole).
func trim(f *os.File) error {
	data, err := io.ReadAll(io.NewSectionReader(f, 0, 1<<62))
	if err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	torn := len(data) > 0 && data[len(data)-1] != '\n'
	lines := bytes.SplitAfter(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) > keepLines {
		lines = lines[len(lines)-keepLines:]
	}
	kept := bytes.Join(lines, nil)
	if torn {
		kept = append(kept, tornMark...)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	if _, err := f.Write(append(kept, '\n')); err != nil { // O_APPEND: at 0 now
		return fmt.Errorf("boots: %w", err)
	}
	return nil
}

// Read returns the boots the file in home records, in the order each was
// first seen. Lines it cannot read are skipped, and so is a last line
// with no end (a write cut short, or still going on); zeros a power cut
// left before a line are dropped. No file is no boots.
func Read(home string) ([]Boot, error) {
	// Read whole: trimming keeps the file near 1 MiB, and a line of any
	// length (zeroed blocks after a power cut) is one line to skip.
	data, err := os.ReadFile(path(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("boots: %w", err)
	}
	var out []Boot
	at := map[string]int{}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[:len(lines)-1] { // the last: "" or unended
		if strings.HasSuffix(line, tornMark) {
			continue
		}
		fl := strings.SplitN(strings.TrimLeft(line, "\x00"), " ", 5)
		if len(fl) < 3 {
			continue
		}
		ts, err := strconv.ParseInt(fl[2], 10, 64)
		if err != nil {
			continue
		}
		var row int64
		switch {
		case fl[1] == "seen" && len(fl) == 3:
		case (fl[1] == "ok" || fl[1] == "bad") && len(fl) >= 4:
			if row, err = strconv.ParseInt(fl[3], 10, 64); err != nil {
				continue
			}
		default:
			continue
		}
		i, ok := at[fl[0]]
		if !ok {
			i = len(out)
			at[fl[0]] = i
			out = append(out, Boot{ID: fl[0]})
		}
		b := &out[i]
		if fl[1] == "seen" {
			b.Seen = time.Unix(ts, 0)
			continue
		}
		b.Verdict, b.At, b.RowID = fl[1], time.Unix(ts, 0), row
		if len(fl) == 5 {
			b.Why = fl[4]
		}
	}
	return out, nil
}

// LastHealthy returns the last boot with an "ok" verdict, this one
// included: a machine that came up healthy after a fix is healthy, and
// the boots that failed before it are past. It also returns the boot's
// index in boots.
func LastHealthy(boots []Boot) (Boot, int, bool) {
	for i := len(boots) - 1; i >= 0; i-- {
		if boots[i].Verdict == "ok" {
			return boots[i], i, true
		}
	}
	return Boot{}, -1, false
}

// Failed reports whether b is a boot that failed: a "bad" verdict, or no
// verdict though it was seen, unless it is the boot current (still on its
// way to multi-user, or a rescue boot).
func (b Boot) Failed(current string) bool {
	return b.Verdict == "bad" || (b.Verdict == "" && !b.Seen.IsZero() && b.ID != current)
}

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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"smartconfig/internal/fsutil"
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
	RowID   int64     // the newest store row at the verdict
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

// appendLine appends one line, after keeping only the last keepLines
// lines when the file has grown past maxSize.
func appendLine(home, line string) error {
	p := path(home)
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxSize {
		if err := trim(p); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("boots: %w", err)
	}
	return f.Close()
}

// trim rewrites p with its last keepLines lines, atomically.
func trim(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("boots: %w", err)
	}
	lines := bytes.SplitAfter(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) > keepLines {
		lines = lines[len(lines)-keepLines:]
	}
	return fsutil.WriteAtomic(p, append(bytes.Join(lines, nil), '\n'), 0o600, os.Geteuid(), os.Getegid())
}

// Read returns the boots the file in home records, in the order each was
// first seen. Lines it cannot read are skipped; no file is no boots.
func Read(home string) ([]Boot, error) {
	f, err := os.Open(path(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("boots: %w", err)
	}
	defer f.Close()
	var out []Boot
	at := map[string]int{}
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		fl := strings.SplitN(lines.Text(), " ", 5)
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
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("boots: %w", err)
	}
	return out, nil
}

// LastHealthy returns the last boot with an "ok" verdict, leaving out the
// boot current (this one: its verdict says nothing about the boot that
// failed before it).
func LastHealthy(boots []Boot, current string) (Boot, bool) {
	for i := len(boots) - 1; i >= 0; i-- {
		if boots[i].Verdict == "ok" && boots[i].ID != current {
			return boots[i], true
		}
	}
	return Boot{}, false
}

// Failed reports whether b is a boot that failed: a "bad" verdict, or no
// verdict though it was seen, unless it is the boot current (still on its
// way to multi-user, or a rescue boot).
func (b Boot) Failed(current string) bool {
	return b.Verdict == "bad" || (b.Verdict == "" && !b.Seen.IsZero() && b.ID != current)
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/spf13/cobra"

	"smartconfig/internal/boot"
	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// Where sc status reads the kernel command line and whether / is mounted
// read-only, and where it looks for a running scd. Tests replace them.
var (
	cmdlinePath  = "/proc/cmdline"
	procDir      = "/proc"
	rootReadOnly = func() bool {
		var st syscall.Statfs_t
		return syscall.Statfs("/", &st) == nil && st.Flags&1 != 0 // ST_RDONLY
	}
)

func newStatusCmd() *cobra.Command {
	var console bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Say what changed since the last healthy boot, and how to undo it",
		Long: `Say which boot this is, which was the last healthy one and whether boots
failed since, and list each recorded file that changed since that boot,
newest first, with the worst problem its change added. For the newest
change that added a blocker or an error, it gives the commands that put
the file back as it was. Exit status 2 when a boot failed since the last
healthy one or a change added a blocker or an error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runStatus(cmd, console) },
	}
	cmd.Flags().BoolVar(&console, "console", false, "the rescue console's form: sc's own rules only, worst first, fits 80x25")
	return cmd
}

// consoleRows is how many changed files the rescue console lists: with
// the lines around them and systemd's five, an 80x25 screen holds it all.
const consoleRows = 5

// Where the kernel lists its consoles and where their devices are, and
// whether a writer is /dev/console itself. Tests replace them.
var (
	consoleActive = "/sys/class/tty/console/active"
	devDir        = "/dev"
	systemConsole = func(w io.Writer) bool {
		f, ok := w.(*os.File)
		if !ok {
			return false
		}
		var st syscall.Stat_t
		return syscall.Fstat(int(f.Fd()), &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFCHR && st.Rdev == 5<<8|1 // 5:1
	}
)

// consoleLimit ends sc status --console. systemd gives rescue.service and
// emergency.service 90 s to start, their ExecStartPre lines included, and
// ends the unit when that runs out: the "-" before the line forgives an
// exit status, not a timeout, and the owner would get no shell at all (the
// chunk D review tried it). The report takes seconds; one that hangs, on
// a sick disk, must give way to the shell well before that.
var consoleLimit = 60 * time.Second

// consoleDeadline ends every console write, counted from the start of sc
// status --console, report or the word that it was stopped: what the
// consoles take after consoleLimit must still end inside systemd's 90 s,
// however many there are, with room for plymouth's ExecStartPre before sc.
var consoleDeadline = 80 * time.Second

// writeConsoles writes the rescue report. To /dev/console (the drop-in:
// rescue.service's tty) it goes to every console the kernel uses, as
// sulogin asks on each: /dev/console alone is only the last console= one
// (a screen with a serial console got no report, the chunk C review).
// Nothing is written after until.
//
// out gets the report only when no console opens: a console that was
// given up is stopped, and out, /dev/console, is one of them (the M4
// final review: a stopped sole console held the write, and the shell,
// past every limit). Even then the write is given up at until.
func writeConsoles(out io.Writer, report []byte, until time.Time) {
	names := []string(nil)
	if systemConsole(out) {
		if b, err := os.ReadFile(consoleActive); err == nil {
			names = strings.Fields(string(b))
		}
	}
	opened := 0
	for _, n := range names {
		f, err := os.OpenFile(filepath.Join(devDir, n), os.O_WRONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		opened++
		writeConsole(ttyFile{f}, report, until)
		f.Close()
	}
	if opened > 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		out.Write(report) // left behind at until; sc exits soon after
		close(done)
	}()
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// A stopped console (Scroll Lock, XOFF, flow control without a peer) must
// not hold up the rescue shell, nor the other consoles: one that moves
// nothing for consoleStall is given up. A slow one is not stopped. A
// serial line whose 4 kB queue is full of boot messages takes no byte of
// the report for 4 s at 9600 baud, and a plain 2 s limit cut the report
// in the middle of a line (the M4 lab, on a loaded host). So a console
// keeps its turn while it takes bytes or its queue drains, for
// consoleTurn at most, and never past consoleDeadline.
//
// stall and turn add up elapsed time, but each slice is capped at
// consoleStep: a host pause freezes the whole machine, and under TCG sc's
// clock jumps forward by the length of the pause (the M4 lab records gaps
// of 10 to 380 s). Uncapped, a pause while a console was only briefly busy
// counts as a long stall and the report never reaches it (bios 2.5 of
// fc47ea3: a 10 s pause as emergency.service started lost the whole report
// on ttyS0). Capped, one pause counts as one slice.
var (
	consoleStall = 5 * time.Second
	consoleTurn  = 10 * time.Second
	consoleSlice = 250 * time.Millisecond
	consoleStep  = 500 * time.Millisecond
)

// console is a console device as writeConsole needs it.
type console interface {
	Write([]byte) (int, error)
	SetWriteDeadline(time.Time) error
	queued() int // bytes taken and not yet sent; -1: not known
}

type ttyFile struct{ *os.File }

func (t ttyFile) queued() int {
	n := -1
	if c, err := t.SyscallConn(); err == nil {
		// Control, not Fd: Fd would make the file blocking again.
		c.Control(func(fd uintptr) {
			var q int32
			if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCOUTQ, uintptr(unsafe.Pointer(&q))); e == 0 {
				n = int(q)
			}
		})
	}
	return n
}

// writeConsole writes report to c and reports whether all of it went; a
// non-zero until ends it at that time, whatever its turn.
func writeConsole(c console, report []byte, until time.Time) bool {
	last := c.queued()
	prev := time.Now()
	var stall, turn time.Duration // elapsed, each slice capped at consoleStep
	for len(report) > 0 {
		if !until.IsZero() && !time.Now().Before(until) {
			return false
		}
		c.SetWriteDeadline(time.Now().Add(consoleSlice))
		n, err := c.Write(report)
		report = report[n:]
		q := c.queued()
		now := time.Now()
		step := now.Sub(prev)
		if step > consoleStep {
			step = consoleStep // a host pause counts as one slice, not its length
		}
		prev = now
		turn += step
		if n > 0 || (q >= 0 && q < last) {
			stall = 0
		} else {
			stall += step
		}
		last = q
		if err == nil {
			continue
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) || stall >= consoleStall || turn >= consoleTurn {
			return false
		}
	}
	return true
}

// fit shortens path from the left ("...") so that a line of prefix and it
// stays within 80 columns; a path that cannot fit is left whole.
func fit(prefix, path string) string {
	room := 80 - len(prefix)
	if len(path) <= room || room < 12 {
		return path
	}
	i := len(path) - (room - 3)
	for i < len(path) && !utf8.RuneStart(path[i]) {
		i++ // never inside a character
	}
	return "..." + path[i:]
}

// screenRows is how many rows text takes on an 80-column console.
func screenRows(text string) int {
	n := 0
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		n += max(1, (utf8.RuneCountInString(l)+79)/80)
	}
	return n
}

// consoleProblem is e's PROBLEM in the console's column: a symlink's
// target is left out, anything else cut to 40 columns.
func consoleProblem(e entry) string {
	p := e.problem
	if e.row.Kind == store.KindLink {
		p = "now a symlink"
	}
	if len(p) > 40 {
		p = p[:37] + "..."
	}
	return p
}

// entry is one changed file of sc status.
type entry struct {
	row     store.Row
	before  *store.Change // the version to compare with and put back; deleted: new since; nil: none known
	problem string
	sev     check.Severity
}

func runStatus(cmd *cobra.Command, console bool) (err error) {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	var report bytes.Buffer
	if console {
		ignoreHangup.Store(true)
		until := time.Now().Add(consoleDeadline)
		real := out
		out = &report
		// A note (the store's repaired copy) goes into the report too:
		// on stderr it would be one more write to a console that may be
		// stopped.
		oldNote := noteOut
		noteOut = &report
		defer func() { noteOut = oldNote }()
		// One of the two writes, never both: the report, or the word that
		// it was stopped.
		var once sync.Once
		stop := time.AfterFunc(consoleLimit, func() {
			once.Do(func() {
				writeConsoles(real, []byte(fmt.Sprintf("sc: the report took over %s and was stopped, so that the shell can start.\n"+
					"Run it from the shell: sc status\n", seconds(consoleLimit))), until)
				// The store's repaired copy and the checks' scratch, in
				// /run or $SC_HOME/tmp: os.Exit runs no deferred cleanup
				// (the M4 final review, B6; review of chunk F, B5).
				check.Stop()
				fsutil.RemovePending()
				os.Exit(1)
			})
		})
		defer func() {
			stop.Stop()
			// An error goes into the report, on every console, not to
			// stderr, which is the last console= one only.
			var code exitCode
			if err != nil && !errors.As(err, &code) {
				fmt.Fprintf(&report, "sc: %v\n", err)
				err = exitCode(1)
			}
			once.Do(func() { writeConsoles(real, report.Bytes(), until) })
		}()
	}
	testHookInStatus()
	home := store.Home()
	cur, _ := boot.CurrentID()
	mode, ro := bootMode(ctx), rootReadOnly()

	boots, bootsErr := boot.Read(home)
	last, li, healthy := boot.LastHealthy(boots)
	var failed []boot.Boot // after the last healthy boot
	for _, b := range boots[li+1:] {
		if b.Failed(cur) {
			failed = append(failed, b)
		}
	}
	// The boundary of "changed since": the last healthy boot whose row is
	// known (a verdict given while the store could not be read has none).
	var from boot.Boot
	bounded := false
	for i := li; i >= 0; i-- {
		if boots[i].Verdict == "ok" && boots[i].RowID >= 0 {
			from, bounded = boots[i], true
			break
		}
	}

	root := "read-write"
	if ro {
		root = "read-only"
	}
	fmt.Fprintf(out, "This boot:     %s (%s), root %s\n", shortBoot(cur), mode, root)
	switch {
	case bootsErr != nil:
		fmt.Fprintf(out, "Last healthy:  unknown (%v)\n", bootsErr)
	case healthy && last.ID == cur:
		fmt.Fprintf(out, "Last healthy:  this boot, %s\n", when(last.At))
	case healthy:
		fmt.Fprintf(out, "Last healthy:  %s, boot %s\n", when(last.At), shortBoot(last.ID))
	default:
		fmt.Fprintln(out, "Last healthy:  none recorded (sc-boot-ok.service records each boot)")
	}
	if len(failed) > 0 {
		f := failed[len(failed)-1]
		at, why := f.Seen, "never reached multi-user"
		if f.Verdict == "bad" {
			at, why = f.At, badWhy(f.Why)
		}
		fmt.Fprintf(out, "Failed since:  %s, last %s: %s\n", count(len(failed), "boot"), at.Local().Format("01-02 15:04"), why)
	}
	fmt.Fprintf(out, "scd:           %s\n", scdState())

	s, err := openStore()
	var none *store.NotInitialisedError
	if errors.As(err, &none) && mode != "normal" {
		// The rescue boot mounts only /: "sc init" would make a new,
		// empty store under /var's mount point (the M4 final review, B4).
		return fmt.Errorf("no store at %s; if /var is a filesystem of its own: mount /var, then sc status", none.Dir)
	}
	if err != nil {
		return err
	}
	defer s.Close()
	var rows []store.Row
	if bounded {
		rows, err = s.Rows(from.RowID, 0)
	} else {
		rows, err = s.Rows(0, 10)
	}
	if err != nil {
		return err
	}
	// One entry per path, its newest row, and the oldest row of the path
	// listed.
	oldest := map[string]store.Row{}
	var entries []entry
	for _, r := range rows {
		if _, ok := oldest[r.Path]; !ok {
			entries = append(entries, entry{row: r})
		}
		oldest[r.Path] = r
	}
	exit := func(worst check.Severity) error {
		if len(failed) > 0 || worst >= check.Error {
			return exitCode(2)
		}
		return nil
	}
	since := "the last healthy boot"
	if healthy && last.ID == cur && from.ID == cur {
		since = "this boot came up"
	}
	switch {
	case len(entries) == 0 && bounded:
		fmt.Fprintf(out, "\nNothing recorded has changed since %s.\n", since)
		return exit(0)
	case len(entries) == 0:
		fmt.Fprintln(out, "\nNothing is recorded yet.")
		return exit(0)
	}

	c, cleanup, err := newChecks()
	if err != nil {
		return err
	}
	defer cleanup()
	testHookStatusChecks()
	if console {
		// The rescue console: sc's own rules only. They need no validator
		// and are fast, so no change goes unjudged for want of time, and
		// a validator cannot hang the console.
		c.Run.Dirs = []string{}
	}
	var worst check.Severity
	for i := range entries {
		e := &entries[i]
		// Compared with the version at the line: the last healthy boot's,
		// or without one, the version before the oldest row listed (a
		// blocker added by the first edit in them is not missed). A path
		// with no version before that is new only with proof of absence
		// ("did not exist"); one first seen after the line, by scd's
		// baseline or a snapshot, is compared with its first version, and
		// with only that, with nothing (the M4 final review, B2: such an
		// fstab was called new, and the undo moved it aside).
		o := oldest[e.row.Path]
		line := o.RowID - 1
		if bounded {
			line = from.RowID
		}
		e.before, err = s.AsOf(e.row.Path, line)
		if err != nil {
			return err
		}
		if e.before == nil && (o.Kind == store.KindDeleted || o.RowID != e.row.RowID) {
			e.before = &o.Change
		}
		e.problem, e.sev = statusProblem(ctx, c, s, e.row, e.before)
		worst = max(worst, e.sev)
	}
	// The undo is for the newest change that added a blocker, else the
	// newest that added an error: a newer error must not take the undo
	// from the blocker that stops the boot (the console lists worst first).
	var undo *entry
	for i := range entries {
		if entries[i].sev >= check.Error && (undo == nil || entries[i].sev > undo.sev) {
			undo = &entries[i]
		}
	}

	// The menu flag is still set, so the next boot shows the menu, when a
	// boot failed since the last healthy one (sc boot seen set it, no ok
	// verdict cleared it), or this boot set it (seen, no verdict: the
	// emergency shell). A rescue boot sets nothing: entered by hand after
	// a healthy boot, the next boot has no menu.
	menuSet := len(failed) > 0
	for _, b := range boots {
		if b.ID == cur && !b.Seen.IsZero() && b.Verdict == "" {
			menuSet = true
		}
	}
	if console {
		// Worst first, so what matters is on screen; then newest first.
		shown := append([]entry(nil), entries...)
		sort.SliceStable(shown, func(i, j int) bool { return shown[i].sev > shown[j].sev })
		var undoText bytes.Buffer
		if undo != nil {
			statusUndo(&undoText, c, *undo, bounded, mode, ro, console, menuSet)
		}
		title := fmt.Sprintf("\nChanged since %s, worst first:\n", since)
		switch {
		case !bounded && healthy:
			// Its verdict was given while the store could not be read
			// (M4 follow-up 7: "no healthy boot" under "Last healthy").
			title = "\nThe newest changes, worst first (not known which came after it):\n"
		case !bounded:
			title = "\nThe newest changes, worst first (no healthy boot is recorded):\n"
		}
		// 20 rows of 25: systemd's prompt takes the other five. The rows
		// already written, the title, the undo and a "more" line first.
		rows := min(consoleRows, max(1, 20-screenRows(report.String()+title)-screenRows(undoText.String())-1))
		fmt.Fprint(out, title)
		w := 0
		for i, e := range shown {
			if i < rows {
				w = max(w, len(consoleProblem(e)))
			}
		}
		for i, e := range shown {
			if i == rows {
				fmt.Fprintf(out, "%s more (sc status lists them all)\n", count(len(shown)-i, "file"))
				break
			}
			// With the date: in S3, "053fdf 21:06" was the evening before
			// (M4 follow-up 7).
			prefix := fmt.Sprintf("%s %s  %-*s  ", e.row.ID, time.Unix(e.row.TS, 0).Local().Format("01-02 15:04"), w, consoleProblem(e))
			fmt.Fprintf(out, "%s%s\n", prefix, fit(prefix, show(e.row.Path)))
		}
		out.Write(undoText.Bytes())
		return exit(worst)
	} else {
		switch {
		case bounded:
			fmt.Fprintf(out, "\nChanged since %s, newest first:\n", since)
		case healthy:
			fmt.Fprintln(out, "\nThe newest changes (which came after the last healthy boot is not known), newest first:")
		default:
			fmt.Fprintln(out, "\nThe newest changes (no healthy boot is recorded), newest first:")
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tWHEN\tFILE\tPROBLEM")
		for _, e := range entries {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.row.ID, when(time.Unix(e.row.TS, 0)), show(e.row.Path), e.problem)
		}
		tw.Flush()
	}
	if undo != nil {
		statusUndo(out, c, *undo, bounded, mode, ro, console, menuSet)
	}
	return exit(worst)
}

// statusProblem is the PROBLEM column of a changed path: for a file with
// a checker, the worst finding its change added, as sc edit judges an
// edit (before is the version it is compared with); else what the row is.
// A file with a checker that is gone is an error: what read it is left
// without it. Not a flag file (nologin, sshd_not_to_be_run) nor
// ld.so.preload: deleting them is what their own findings ask for, and
// the undo would put them back (the M4 final review, B3).
func statusProblem(ctx context.Context, c *check.Checks, s *store.Store, r store.Row, before *store.Change) (string, check.Severity) {
	checker := c.GraphInUse().Checker(r.Path)
	checked := checker != ""
	switch r.Kind {
	case store.KindDeleted:
		if checked && checker != "flag" && checker != "preload" && before != nil && before.Kind != store.KindDeleted {
			return "error: deleted", check.Error
		}
		return "deleted", 0
	case store.KindLink:
		// A checked file a link replaced: its checker does not judge what
		// the boot reads now (the M4 final review, B9), so an error, whose
		// undo puts the file back.
		if checked && before != nil && before.Kind == store.KindFile {
			return "error: now a symlink to " + show(r.Target) + ", not checked", check.Error
		}
		return "now a symlink to " + show(r.Target), 0
	case store.KindDigest:
		return "changed (fingerprint only)", 0
	}
	if !checked {
		return "-", 0
	}
	data, err := s.Blob(r.Blob)
	if err != nil {
		return "not checked (content missing)", 0
	}
	rep, err := c.CheckSaved(ctx, r.Path, data)
	if err != nil {
		return "not checked", 0
	}
	var had []check.Finding
	if before != nil && before.Kind == store.KindFile {
		if b, err := s.Blob(before.Blob); err == nil {
			if hr, err := c.CheckSaved(ctx, r.Path, b); err == nil {
				had = hr.Findings
			}
		}
	}
	added := check.Added(had, rep.Findings)
	w := check.Worst(added)
	if w == 0 {
		switch {
		case rep.Unchecked:
			return "not checked", 0
		case rep.Incomplete:
			return "not fully checked", 0
		}
		return "no problem found", 0
	}
	for _, f := range added {
		if f.Severity == w {
			where := ""
			if f.Line > 0 {
				where = fmt.Sprintf(", line %d", f.Line)
			}
			return fmt.Sprintf("%s %s%s", w, f.Rule, where), w
		}
	}
	return "", w
}

// statusUndo prints the commands that undo e's change: put back the
// version e.before (or move a new file aside), then, in the rescue or
// emergency boot, reboot. After an fstab failure a plain reboot waits for
// the missing disk again: daemon-reload first (the M4 lab).
func statusUndo(out io.Writer, c *check.Checks, e entry, bounded bool, mode string, ro, console, menuSet bool) {
	path := show(e.row.Path)
	as := " as it was before this change"
	if bounded {
		as = " as it was during the last healthy boot"
	}
	if console {
		as = "" // 80 columns: the version is the one named below
	}
	shown := func(prefix string) string {
		if console {
			return fit(prefix, path)
		}
		return path
	}
	if e.before == nil {
		// Never "move it aside": without proof that it is new, it may be
		// a file the boot needs (fstab).
		fmt.Fprintf(out, "\nNo earlier version of %s is recorded: fix it by hand.\n", shown("No earlier version of  is recorded: fix it by hand."))
		return
	}
	if e.before.Kind == store.KindDeleted {
		fmt.Fprintf(out, "\nTo undo %s, which is new, move it aside:\n", shown("To undo , which is new, move it aside:"))
	} else {
		fmt.Fprintf(out, "\nTo put %s back%s:\n", shown("To put  back"+as+":"), as)
	}
	if ro {
		fmt.Fprintln(out, "  mount -o remount,rw /")
	}
	switch {
	case e.before.Kind == store.KindDeleted && console:
		// Short lines: a console line that wraps pushes the top off.
		dir, name := filepath.Split(e.row.Path)
		fmt.Fprintf(out, "  cd %s\n  mv %s %s\n", shellQuote(dir), shellQuote(name), shellQuote(name+".sc-off"))
	case e.before.Kind == store.KindDeleted:
		// Every reader of a directory sc checks skips this name: *.yaml,
		// *.conf, *.rules, units, and sudoers.d's names with a dot.
		fmt.Fprintf(out, "  mv %s %s\n", shellQuote(e.row.Path), shellQuote(e.row.Path+".sc-off"))
	default:
		fmt.Fprintf(out, "  sc restore %s\n", e.before.ID)
	}
	fmt.Fprintln(out, "  sync")
	if mode != "normal" {
		fmt.Fprintln(out, "  systemctl daemon-reload\n  systemctl reboot")
		if console && mode == "emergency" {
			// Ubuntu may start a login prompt on the emergency shell's
			// console, and sometimes only that is left (5 of 11 failed
			// boots in the M4 lab had one; the rescue entry's shell never).
			fmt.Fprintln(out, `At a "login:" prompt instead of "#": log in, then put sudo before each.`)
		}
		// A rescue boot cannot clear the menu flag (/boot is not mounted).
		if menuSet {
			fmt.Fprintln(out, "The menu shows once more: the first entry, Ubuntu, is the one.")
		}
	} else if a := c.GraphInUse().Apply(e.row.Path); a != "" {
		fmt.Fprintf(out, "It takes effect %s.\n", a)
	}
}

// shellQuote is s as a shell word that the owner can type as shown: as
// it is when nothing in it is special, else in single quotes, or as $'...'
// with control characters escaped (the M4 final review, B7: a name with a
// space or a ";" broke the command, or ran another).
func shellQuote(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/+-:@%,=", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	if !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		var b strings.Builder
		b.WriteString("$'")
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\\' || c == '\'':
				b.WriteByte('\\')
				b.WriteByte(c)
			case c < 0x20 || c >= 0x7f: // C1 controls are two bytes of UTF-8
				fmt.Fprintf(&b, "\\x%02x", c)
			default:
				b.WriteByte(c)
			}
		}
		return b.String() + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// badWhy is a "bad" verdict's reason in words.
func badWhy(why string) string {
	var out []string
	for _, f := range strings.Fields(why) {
		switch f {
		case "emergency=active", "emergency=activating":
			out = append(out, "emergency mode")
		case "rescue=active", "rescue=activating":
			out = append(out, "rescue mode")
		default:
			if strings.HasPrefix(f, "local-fs=") && f != "local-fs=active" {
				out = append(out, "a mount failed")
			}
		}
	}
	if len(out) == 0 {
		return why
	}
	return strings.Join(out, ", ")
}

// bootMode is "rescue" or "emergency" when systemd is in that mode or the
// kernel command line asked for it, else "normal". In the drop-in, sc
// status runs while emergency.service or rescue.service is activating; its
// target is reached only after it (targets are never "activating").
func bootMode(ctx context.Context) string {
	units := []string{"emergency.service", "emergency.target", "rescue.service", "rescue.target"}
	if states, err := systemctl(ctx, append([]string{"is-active"}, units...)...); err == nil && len(states) == len(units) {
		for i, u := range units {
			if states[i] == "active" || states[i] == "activating" {
				return strings.TrimSuffix(strings.TrimSuffix(u, ".service"), ".target")
			}
		}
	}
	b, _ := os.ReadFile(cmdlinePath)
	for _, w := range strings.Fields(string(b)) {
		switch w {
		case "systemd.unit=emergency.target", "emergency", "-b":
			return "emergency"
		case "systemd.unit=rescue.target", "rescue", "single", "s", "S", "1", "recovery":
			return "rescue"
		}
	}
	return "normal"
}

// scdState says whether scd runs: a plain "sc watch" (a user's sc watch
// --root watches something else), found in /proc, since probing its lock
// could make a starting scd find it taken.
func scdState() string {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return "unknown"
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
		if len(args) == 2 && filepath.Base(args[0]) == "sc" && args[1] == "watch" {
			return "running (pid " + e.Name() + ")"
		}
	}
	return "not running"
}

// seconds is d as "60 s" when it is whole seconds, else as Go writes it
// ("300ms", in tests), never "1m0s".
func seconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return d.String()
}

// shortBoot is a boot id short enough for a line: its first 8 hex digits.
func shortBoot(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		return "unknown"
	}
	return id
}

func when(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

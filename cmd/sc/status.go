package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"smartconfig/internal/boot"
	"smartconfig/internal/check"
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
// the lines around them and systemd's own, an 80x25 screen holds it all.
const consoleRows = 6

// entry is one changed file of sc status.
type entry struct {
	row     store.Row
	before  *store.Change // the version to compare with and put back; nil: new since
	problem string
	sev     check.Severity
}

func runStatus(cmd *cobra.Command, console bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
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
	// One entry per path, its newest row. Without a healthy boot, it is
	// compared with the oldest version of the path among the rows listed
	// (a blocker added by an earlier edit in them is not missed), or, with
	// one row, the version before it.
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
	if console {
		// The rescue console: sc's own rules only. They need no validator
		// and are fast, so no change goes unjudged for want of time, and
		// a validator cannot hang the console.
		c.Run.Dirs = []string{}
	}
	var worst check.Severity
	for i := range entries {
		e := &entries[i]
		switch o := oldest[e.row.Path]; {
		case bounded:
			e.before, err = s.AsOf(e.row.Path, from.RowID)
		case o.RowID != e.row.RowID:
			e.before = &o.Change
		default:
			e.before, err = s.AsOf(e.row.Path, e.row.RowID-1)
		}
		if err != nil {
			return err
		}
		e.problem, e.sev = statusProblem(ctx, c, s, e.row, e.before)
		worst = max(worst, e.sev)
	}
	// The undo is for the newest change that added a blocker or an error.
	var undo *entry
	for i := range entries {
		if entries[i].sev >= check.Error {
			undo = &entries[i]
			break
		}
	}

	if console {
		// Worst first, so what matters is on screen; then newest first.
		shown := append([]entry(nil), entries...)
		sort.SliceStable(shown, func(i, j int) bool { return shown[i].sev > shown[j].sev })
		fmt.Fprintf(out, "\nChanged since %s, worst first:\n", since)
		w := 0
		for i, e := range shown {
			if i < consoleRows {
				w = max(w, len(e.problem))
			}
		}
		for i, e := range shown {
			if i == consoleRows {
				fmt.Fprintf(out, "%s more (sc status lists them all)\n", count(len(shown)-i, "file"))
				break
			}
			fmt.Fprintf(out, "%s %s  %-*s  %s\n", e.row.ID, time.Unix(e.row.TS, 0).Local().Format("15:04"), w, e.problem, show(e.row.Path))
		}
	} else {
		if bounded {
			fmt.Fprintf(out, "\nChanged since %s, newest first:\n", since)
		} else {
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
		statusUndo(out, c, *undo, bounded, mode, ro)
	}
	return exit(worst)
}

// statusProblem is the PROBLEM column of a changed path: for a file with
// a checker, the worst finding its change added, as sc edit judges an
// edit (before is the version it is compared with); else what the row is.
// A file with a checker that is gone is an error: what read it is left
// without it.
func statusProblem(ctx context.Context, c *check.Checks, s *store.Store, r store.Row, before *store.Change) (string, check.Severity) {
	checked := c.GraphInUse().Checker(r.Path) != ""
	switch r.Kind {
	case store.KindDeleted:
		if checked && before != nil && before.Kind != store.KindDeleted {
			return "error: deleted", check.Error
		}
		return "deleted", 0
	case store.KindLink:
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
		if rep.Incomplete {
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
func statusUndo(out io.Writer, c *check.Checks, e entry, bounded bool, mode string, ro bool) {
	path := show(e.row.Path)
	as := "as it was before this change"
	if bounded {
		as = "as it was during the last healthy boot"
	}
	if e.before == nil || e.before.Kind == store.KindDeleted {
		fmt.Fprintf(out, "\nTo undo %s, which is new, move it aside:\n", path)
	} else {
		fmt.Fprintf(out, "\nTo put %s back %s:\n", path, as)
	}
	if ro {
		fmt.Fprintln(out, "  mount -o remount,rw /")
	}
	if e.before == nil || e.before.Kind == store.KindDeleted {
		// Every reader of a directory sc checks skips this name: *.yaml,
		// *.conf, *.rules, units, and sudoers.d's names with a dot.
		fmt.Fprintf(out, "  mv %s %s.sc-off\n", path, path)
	} else {
		fmt.Fprintf(out, "  sc restore %s\n", e.before.ID)
	}
	fmt.Fprintln(out, "  sync")
	if mode != "normal" {
		fmt.Fprintln(out, "  systemctl daemon-reload\n  systemctl reboot")
	} else if a := c.GraphInUse().Apply(e.row.Path); a != "" {
		fmt.Fprintf(out, "It takes effect %s.\n", a)
	}
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

// bootMode is "rescue" or "emergency" when systemd is in that mode (sc
// status in emergency.service's drop-in) or the kernel command line asked
// for it, else "normal".
func bootMode(ctx context.Context) string {
	if states, err := systemctl(ctx, "is-active", "emergency.target", "rescue.target"); err == nil && len(states) == 2 {
		for i, m := range []string{"emergency", "rescue"} {
			if states[i] == "active" || states[i] == "activating" {
				return m
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

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	cmd.Flags().BoolVar(&console, "console", false, "the rescue console's form: at most 10 s and 20 changes")
	return cmd
}

func runStatus(cmd *cobra.Command, console bool) error {
	ctx := cmd.Context()
	limit := 0
	if console {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		limit = 20
	}
	out := cmd.OutOrStdout()
	home := store.Home()
	cur, _ := boot.CurrentID()
	rescue, ro := rescueBoot(), rootReadOnly()

	boots, bootsErr := boot.Read(home)
	last, healthy := boot.LastHealthy(boots, cur)
	var failed []boot.Boot // since the last healthy boot
	for i, b := range boots {
		if healthy && b.ID == last.ID {
			failed = nil // only those after it
			continue
		}
		if b.Failed(cur) {
			failed = append(failed, boots[i])
		}
	}

	mode, root := "normal", "read-write"
	if rescue {
		mode = "rescue"
	}
	if ro {
		root = "read-only"
	}
	fmt.Fprintf(out, "This boot:     %s (%s), root %s\n", shortBoot(cur), mode, root)
	switch {
	case bootsErr != nil:
		fmt.Fprintf(out, "Last healthy:  unknown (%v)\n", bootsErr)
	case healthy:
		fmt.Fprintf(out, "Last healthy:  %s, boot %s\n", when(last.At), shortBoot(last.ID))
	default:
		fmt.Fprintln(out, "Last healthy:  none recorded (sc-boot-ok.service records each boot)")
	}
	if len(failed) > 0 {
		f := failed[len(failed)-1]
		why, at := "never reached multi-user", f.Seen
		if f.Verdict == "bad" {
			why, at = f.Why, f.At
		}
		fmt.Fprintf(out, "Failed since:  %s, the last at %s: %s\n", count(len(failed), "boot"), when(at), why)
	}
	fmt.Fprintf(out, "scd:           %s\n", scdState())

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	var rows []store.Row
	if healthy {
		rows, err = s.Rows(last.RowID, 0)
	} else {
		rows, err = s.Rows(0, 10)
	}
	if err != nil {
		return err
	}
	var changed []store.Row // the newest row of each path
	seen := map[string]bool{}
	for _, r := range rows {
		if !seen[r.Path] {
			seen[r.Path] = true
			changed = append(changed, r)
		}
	}
	exit := func(worst check.Severity) error {
		if len(failed) > 0 || worst >= check.Error {
			return exitCode(2)
		}
		return nil
	}
	switch {
	case len(changed) == 0 && healthy:
		fmt.Fprintln(out, "\nNothing recorded has changed since the last healthy boot.")
		return exit(0)
	case len(changed) == 0:
		fmt.Fprintln(out, "\nNothing is recorded yet.")
		return exit(0)
	case healthy:
		fmt.Fprintln(out, "\nChanged since the last healthy boot, newest first:")
	default:
		fmt.Fprintln(out, "\nThe newest changes (no healthy boot is recorded), newest first:")
	}

	c, cleanup, err := newChecks()
	if err != nil {
		return err
	}
	defer cleanup()
	// Every change is judged, also past the console's 20 lines: the exit
	// status and the undo must not miss a blocker that is not listed.
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWHEN\tFILE\tPROBLEM")
	var worst check.Severity
	var undoPath, undoID string
	for i, r := range changed {
		at := r.RowID - 1 // what the path was before this change
		if healthy {
			at = last.RowID // what it was during the last healthy boot
		}
		before, err := s.AsOf(r.Path, at)
		if err != nil {
			return err
		}
		problem, sev := statusProblem(ctx, c, s, r, before)
		if limit == 0 || i < limit {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, when(time.Unix(r.TS, 0)), show(r.Path), problem)
		}
		worst = max(worst, sev)
		if sev >= check.Error && undoID == "" && before != nil {
			undoPath, undoID = r.Path, before.ID
		}
	}
	tw.Flush()
	if limit > 0 && len(changed) > limit {
		fmt.Fprintf(out, "%s more (sc status lists them all)\n", count(len(changed)-limit, "file"))
	}
	if undoID != "" {
		statusUndo(out, c, undoPath, undoID, healthy, rescue, ro)
	}
	return exit(worst)
}

// statusProblem is the PROBLEM column of a changed path: for a file with
// a checker, the worst finding its change added, as sc edit judges an
// edit (before is the version it is compared with); else what the row is.
func statusProblem(ctx context.Context, c *check.Checks, s *store.Store, r store.Row, before *store.Change) (string, check.Severity) {
	switch r.Kind {
	case store.KindDeleted:
		return "deleted", 0
	case store.KindLink:
		return "now a symlink to " + show(r.Target), 0
	case store.KindDigest:
		return "changed (fingerprint only)", 0
	}
	if c.GraphInUse().Checker(r.Path) == "" {
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

// statusUndo prints the commands that put path back as row id has it.
func statusUndo(out io.Writer, c *check.Checks, path, id string, healthy, rescue, ro bool) {
	as := "as it was before this change"
	if healthy {
		as = "as it was during the last healthy boot"
	}
	fmt.Fprintf(out, "\nTo put %s back %s:\n", show(path), as)
	if ro {
		fmt.Fprintln(out, "  mount -o remount,rw /")
	}
	fmt.Fprintf(out, "  sc restore %s\n  sync\n", id)
	switch {
	case rescue:
		fmt.Fprintln(out, "  systemctl reboot")
	default:
		if a := c.GraphInUse().Apply(path); a != "" {
			fmt.Fprintf(out, "It takes effect %s.\n", a)
		}
	}
}

// rescueBoot reports whether this boot was asked for rescue or emergency
// mode on the kernel command line (the SmartConfig rescue entry, or by
// hand).
func rescueBoot() bool {
	b, _ := os.ReadFile(cmdlinePath)
	for _, w := range strings.Fields(string(b)) {
		switch w {
		case "systemd.unit=rescue.target", "systemd.unit=emergency.target", "rescue", "emergency", "single", "s", "S", "1":
			return true
		}
	}
	return false
}

// scdState says whether an sc watch runs, from /proc: probing its lock
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
		args := strings.Split(string(b), "\x00")
		if len(args) > 1 && filepath.Base(args[0]) == "sc" && args[1] == "watch" {
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

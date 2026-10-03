package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// testHookChecks, if set by a test, adjusts the checks a command runs (its
// graph and where validators are looked up).
var testHookChecks func(*check.Checks)

// rowID is what an id or id prefix looks like to sc cat and sc restore. An
// argument of sc check is an id when it looks like one and no file of that
// name exists.
var rowID = regexp.MustCompile(`^[0-9a-fA-F]{1,6}$`)

func isRowID(arg string) bool {
	if _, err := os.Lstat(arg); err == nil {
		return false
	}
	return rowID.MatchString(arg)
}

func newCheckCmd() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "check [path|id]...",
		Short: "Check config files, or saved versions, for problems",
		Long: `Check config files for problems before they bite. With no argument, every
file on this machine that sc has a checker for; an id checks that saved
version. Exit status 2 when a file has a blocker or an error, 1 when a
file could not be checked.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheck(cmd, args, verbose)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "explain each rule and show the validators' own lines")
	return cmd
}

// checkTarget is one thing to check: a file as it is now, or a saved row.
type checkTarget struct {
	label string // as shown: the path, or "path (id)"
	path  string
	data  []byte
}

func runCheck(cmd *cobra.Command, args []string, verbose bool) error {
	home, cleanup, err := scratchHome()
	if err != nil {
		return err
	}
	defer cleanup()
	c := &check.Checks{Home: home}
	if testHookChecks != nil {
		testHookChecks(c)
	}
	out := cmd.OutOrStdout()
	var targets []checkTarget
	var notes []string
	unchecked := 0 // files with a checker that could not be read
	if len(args) == 0 {
		g := c.Graph
		if g == nil {
			g = check.DefaultGraph()
		}
		for _, p := range g.Files() {
			st, err := fsutil.ReadState(p)
			switch {
			case errors.Is(err, os.ErrPermission):
				unchecked++
				notes = append(notes, show(p)+": not checked: permission denied (run sc check as root)")
			case err != nil:
				unchecked++
				notes = append(notes, show(p)+": not checked: "+err.Error())
			case st.Kind == "file":
				targets = append(targets, checkTarget{p, p, st.Data})
			}
		}
	}
	var s *store.Store
	for _, a := range args {
		if isRowID(a) {
			if s == nil {
				if s, err = openStore(); err != nil {
					return err
				}
				defer s.Close()
			}
			row, err := s.Get(a)
			if err != nil {
				return err
			}
			if row.Kind == store.KindLink {
				return fmt.Errorf("%s is a link row of %s; sc check reads files", row.ID, row.Path)
			}
			data, err := rowContent(s, row)
			if err != nil {
				return err
			}
			targets = append(targets, checkTarget{row.Path + " (" + row.ID + ")", row.Path, data})
			continue
		}
		p, err := filepath.Abs(a)
		if err != nil {
			return err
		}
		st, err := fsutil.ReadState(p)
		if err != nil {
			return err
		}
		if st.Kind != "file" {
			return fmt.Errorf("%s is a symlink; sc check reads files", p)
		}
		targets = append(targets, checkTarget{p, p, st.Data})
	}

	var rows []findingRow
	checked := 0
	for _, t := range targets {
		rep, err := c.Check(cmd.Context(), t.path, t.data)
		if err != nil {
			return err
		}
		if rep.Checker == "" {
			notes = append(notes, show(t.label)+": no checker reads this file")
			continue
		}
		checked++
		for _, f := range rep.Findings {
			rows = append(rows, findingRow{t.label, f})
		}
		for _, n := range rep.Notes {
			notes = append(notes, show(t.label)+": "+n)
		}
	}

	n, err := findingsTable(out, rows)
	if err != nil {
		return err
	}
	for _, note := range notes {
		fmt.Fprintln(out, "note: "+note)
	}
	// A file that was not checked is not a clean file: exit 1, unless a
	// finding already makes it 2.
	var notChecked error
	if unchecked > 0 {
		notChecked = fmt.Errorf("%s not checked (see the notes)", count(unchecked, "file"))
	}
	if len(rows) == 0 {
		switch {
		case checked == 0 && len(notes) == 0:
			fmt.Fprintln(out, "no files to check")
		case checked > 0:
			fmt.Fprintf(out, "no problems found in %s\n", count(checked, "file"))
		}
		return notChecked
	}
	fmt.Fprintf(out, "%s in %s.\n", tally(n), count(checked, "file"))
	if verbose {
		fs := make([]check.Finding, len(rows))
		for i, r := range rows {
			fs[i] = r.f
		}
		explain(out, fs)
	} else {
		fmt.Fprintln(out, "sc check -v explains; sc log FILE lists the versions to restore.")
	}
	if n[check.Blocker]+n[check.Error] > 0 {
		return exitCode(2)
	}
	return notChecked
}

// findingRow is one line of the findings table.
type findingRow struct {
	label string // the file as shown: its path, or "path (id)"
	f     check.Finding
}

// findingsTable prints the table of rows (nothing when there are none) and
// returns how many findings of each severity it holds.
func findingsTable(out io.Writer, rows []findingRow) (n [check.Blocker + 1]int, err error) {
	if len(rows) == 0 {
		return n, nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tFILE\tLINE\tRULE\tPROBLEM")
	for _, r := range rows {
		line := "-"
		if r.f.Line > 0 {
			line = fmt.Sprint(r.f.Line)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.f.Severity, show(r.label), line, r.f.Rule, show(r.f.Text))
		n[r.f.Severity]++
	}
	return n, tw.Flush()
}

// tally is "1 blocker, 2 warnings" for the counts findingsTable returns.
func tally(n [check.Blocker + 1]int) string {
	var parts []string
	for _, sev := range []check.Severity{check.Blocker, check.Error, check.Warning} {
		if n[sev] > 0 {
			parts = append(parts, count(n[sev], sev.String()))
		}
	}
	return strings.Join(parts, ", ")
}

// explain prints each rule's explanation once, in the order the findings
// came, and the validators' own lines.
func explain(out io.Writer, fs []check.Finding) {
	seen := map[string]bool{}
	for _, f := range fs {
		if r, ok := check.Lookup(f.Rule); ok && !seen[f.Rule] {
			seen[f.Rule] = true
			fmt.Fprintf(out, "\n%s:\n", f.Rule)
			for _, l := range strings.Split(strings.TrimRight(r.Explain, "\n"), "\n") {
				fmt.Fprintln(out, "  "+l)
			}
		}
	}
	first := true
	for _, f := range fs {
		if f.Raw == "" {
			continue
		}
		if first {
			fmt.Fprintln(out, "\nfrom the validators:")
			first = false
		}
		fmt.Fprintf(out, "  %s:%d: %s\n", show(f.Path), f.Line, show(f.Raw))
	}
}

// count is "1 file" or "3 files".
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// scratchHome returns where the validators' scratch copies go: $SC_HOME
// when this user may write there (root), else a private directory of its
// own that cleanup removes, so a file the user can read can be checked
// without sudo.
func scratchHome() (dir string, cleanup func(), err error) {
	home := store.Home()
	if os.MkdirAll(filepath.Join(home, "tmp"), 0o700) == nil && syscall.Access(filepath.Join(home, "tmp"), 2 /* W_OK */) == nil {
		return home, func() {}, nil
	}
	dir, err = os.MkdirTemp("", "sc-check-")
	if err != nil {
		return "", nil, fmt.Errorf("scratch directory: %w", err)
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

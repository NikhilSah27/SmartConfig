package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// restoreStdin is where sc restore reads its answer from; the tests
// replace it.
var restoreStdin io.Reader = os.Stdin

func newRestoreCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "restore <id>",
		Short: "Write a snapshot back with its original mode and owner",
		Long: `Write a snapshot back with its original mode and owner. The file as it
is now is saved first, so the restore can itself be undone.

A file sc has a checker for is checked first: when the version to write
adds a blocker or an error compared with the file as it is now, sc
restore lists them and asks (end of input means no: nothing is written,
exit 2). --force writes without checking.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			src, err := s.Get(args[0])
			if err != nil {
				return err
			}
			// A separate /boot not mounted (the rescue shell mounts only
			// /): the write would land on the root filesystem's copy,
			// which nothing reads, and say it worked.
			if fstabMountsFile(src.Path) {
				return fmt.Errorf("%s is itself a mount point in /etc/fstab: put back the file mounted on it instead (file not changed)", show(src.Path))
			}
			if mps := unmountedMounts(src.Path); len(mps) > 0 {
				return fmt.Errorf("%s is under %s, which /etc/fstab mounts and is not mounted: run %s, then this again (file not changed)",
					show(src.Path), show(mps[len(mps)-1]), mountCommands(mps, " && "))
			}
			out := cmd.OutOrStdout()
			if !force && src.Kind == store.KindFile {
				ok, err := restoreChecked(cmd, s, src)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintf(out, "not restored: %s is unchanged\n", show(src.Path))
					return exitCode(2)
				}
			}
			// Only the writing is a mutating command: a signal at the
			// question ends sc at once.
			mutating.Store(true)
			c, prev, err := s.Restore(src.ID)
			if err != nil {
				return err
			}
			if c.ID == "" {
				fmt.Fprintf(out, "nothing to do: %s is already absent, as in %s\n", show(src.Path), src.ID)
				return nil
			}
			saved := "no previous file existed"
			if prev != nil {
				saved = "previous state saved as " + prev.ID
			}
			switch c.Kind {
			case store.KindLink:
				fmt.Fprintf(out, "restored %s from %s (link -> %s, owner %s:%s), %s\n",
					show(c.Path), src.ID, show(c.Target), userName(c.UID), groupName(c.GID), saved)
			case store.KindDeleted:
				fmt.Fprintf(out, "removed %s to match %s (%s), %s\n", show(c.Path), src.ID, absenceWord(src), saved)
			default:
				fmt.Fprintf(out, "restored %s from %s (mode %s %s:%s), %s\n",
					show(c.Path), src.ID, octalMode(c.Mode), userName(c.UID), groupName(c.GID), saved)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "restore without checking the version first")
	return cmd
}

// restoreChecked checks the version src would write against the file at
// its path as it is now (M5 follow-up 3), as sc edit checks an edit. It
// reports whether to go on: yes when the version adds no blocker or
// error, when the path has no checker, or when the owner says y. A check
// that cannot run does not stop a restore: it says so on stderr and goes
// on (a rescue shell may lack a validator, or a place for scratch files).
// On a read-only root, as on the rescue console, only sc's own rules run.
func restoreChecked(cmd *cobra.Command, s *store.Store, src store.Change) (bool, error) {
	c, cleanup, err := newChecks()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "sc: note: %s not checked (%v)\n", show(src.Path), err)
		return true, nil
	}
	defer cleanup()
	if c.GraphInUse().Checker(src.Path) == "" {
		return true, nil
	}
	if rootReadOnly() {
		c.Run.Dirs = []string{}
	}
	data, err := s.Blob(src.Blob)
	if err != nil {
		return false, err
	}
	var had []check.Finding
	st, err := fsutil.ReadState(src.Path)
	switch {
	case fsutil.IsNotExist(err):
	case err != nil:
		return false, err
	case st.Kind == "file": // a link: restore refuses it itself
		rep, err := c.Check(cmd.Context(), src.Path, st.Data)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "sc: note: %s not checked (%v)\n", show(src.Path), err)
			return true, nil
		}
		had = rep.Findings
	}
	rep, err := c.Check(cmd.Context(), src.Path, data)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "sc: note: %s not checked (%v)\n", show(src.Path), err)
		return true, nil
	}
	added := check.Added(had, rep.Findings)
	if check.Worst(added) < check.Error {
		return true, nil
	}
	out := cmd.OutOrStdout()
	rows := make([]findingRow, len(added))
	for i, f := range added {
		rows[i] = findingRow{src.Path, f}
	}
	n, err := findingsTable(out, rows)
	if err != nil {
		return false, err
	}
	explain(out, added)
	fmt.Fprintf(out, "\nRestoring %s adds %s compared with %s as it is now.\n", src.ID, tally(n), show(src.Path))
	fmt.Fprint(out, "Restore anyway? [y/N]: ")
	answer, _ := bufio.NewReader(restoreStdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	if !strings.HasSuffix(answer, "\n") {
		fmt.Fprintln(out) // end of input: the question's line ends here
	}
	return false, nil
}

// octalMode formats permissions plus setuid/setgid/sticky as chmod(1) octal.
func octalMode(m os.FileMode) string {
	v := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m&os.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m&os.ModeSticky != 0 {
		v |= 0o1000
	}
	return fmt.Sprintf("%04o", v)
}

func userName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

func groupName(gid int) string {
	if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
		return g.Name
	}
	return strconv.Itoa(gid)
}

// absenceWord is "did not exist" or "deleted", for the removal line.
func absenceWord(c store.Change) string {
	if absence(c) == "did not exist" {
		return "did not exist"
	}
	return "deleted"
}

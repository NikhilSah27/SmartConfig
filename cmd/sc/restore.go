package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/spf13/cobra"

	"smartconfig/internal/store"
)

func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id>",
		Short: "Write a snapshot back with its original mode and owner",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mutating.Store(true)
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
			c, prev, err := s.Restore(src.ID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
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

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/spf13/cobra"
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
			c, prev, err := s.Restore(src.ID)
			if err != nil {
				return err
			}
			saved := "no previous file existed"
			if prev != nil {
				saved = "previous state saved as " + prev.ID
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %s from %s (mode %s %s:%s), %s\n",
				c.Path, src.ID, octalMode(c.Mode), userName(c.UID), groupName(c.GID), saved)
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

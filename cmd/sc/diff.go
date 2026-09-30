package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <id> [<id2>]",
		Short: "Diff a snapshot against the file on disk, or two snapshots",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			a, aData, err := loadSnapshot(s, args[0])
			if err != nil {
				return err
			}
			aLabel := label("a", a.Path, "snapshot "+a.ID)
			var bData []byte
			var bLabel string
			if len(args) == 2 {
				b, data, err := loadSnapshot(s, args[1])
				if err != nil {
					return err
				}
				bData, bLabel = data, label("b", b.Path, "snapshot "+b.ID)
			} else {
				st, err := fsutil.ReadState(a.Path)
				if err != nil {
					return err
				}
				bData = st.Data
				if st.Kind == "link" {
					bData = []byte(st.Target + "\n")
				}
				bLabel = label("b", a.Path, "on disk")
			}
			d, err := store.UnifiedDiff(aData, bData, aLabel, bLabel)
			if err != nil {
				return fmt.Errorf("diff: %w", err)
			}
			out := cmd.OutOrStdout()
			if d == "" {
				fmt.Fprintln(out, "no differences")
				return nil
			}
			_, err = io.WriteString(out, d)
			return err
		},
	}
}

func loadSnapshot(s *store.Store, id string) (store.Change, []byte, error) {
	c, err := s.Get(id)
	if err != nil {
		return store.Change{}, nil, err
	}
	data, err := rowContent(s, c)
	return c, data, err
}

// label builds "a/etc/hosts (snapshot 1a2b3c)"; the path's leading slash is
// dropped so the prefix reads like git's.
func label(side, path, what string) string {
	return side + "/" + strings.TrimPrefix(path, "/") + " (" + what + ")"
}

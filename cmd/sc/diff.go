package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/redact"
	"smartconfig/internal/store"
)

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <id> [<id2>]",
		Short: "Diff a snapshot against the file on disk, or two snapshots",
		Long: `Diff a snapshot against the file on disk, or two snapshots. In a secret
file (sc scope says which) each secret value is shown as <hidden N>: the
same N on both sides is the same value.`,
		Args: cobra.RangeArgs(1, 2),
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
			var bLabel, bPath string
			if len(args) == 2 {
				b, data, err := loadSnapshot(s, args[1])
				if err != nil {
					return err
				}
				bData, bLabel, bPath = data, label("b", b.Path, "snapshot "+b.ID), b.Path
			} else {
				st, err := fsutil.ReadState(a.Path)
				if err != nil {
					return err
				}
				bData = st.Data
				if st.Kind == "link" {
					bData = []byte(st.Target + "\n")
				}
				bLabel, bPath = label("b", a.Path, "on disk"), a.Path
			}
			// One numbering for both sides, so a changed secret shows.
			hide := redact.Numbered()
			aShown, na := hideSecrets(a.Path, aData, hide)
			bShown, nb := hideSecrets(bPath, bData, hide)
			d, err := store.UnifiedDiff(aShown, bShown, aLabel, bLabel)
			if err != nil {
				return fmt.Errorf("diff: %w", err)
			}
			out := cmd.OutOrStdout()
			switch {
			case d == "" && !bytes.Equal(aData, bData):
				// Blanks around a hidden value, which the marker drops.
				fmt.Fprintln(out, "no differences shown: the two differ only where values are hidden")
			case d == "":
				fmt.Fprintln(out, "no differences")
			default:
				if _, err := io.WriteString(out, d); err != nil {
					return err
				}
			}
			if na > 0 {
				hiddenNote(a.Path, "<hidden N>")
			}
			if nb > 0 && bPath != a.Path {
				hiddenNote(bPath, "<hidden N>")
			}
			return nil
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

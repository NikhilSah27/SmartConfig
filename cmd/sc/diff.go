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
	cmd := &cobra.Command{
		Use:   "diff <id> [<id2>]",
		Short: "Diff a snapshot against the file on disk, or two snapshots",
		Long: `Diff a snapshot against the file on disk, or two snapshots. In a secret
file (sc scope says which) a secret value is shown as <hidden> when the
other version hides the same value too, anywhere in it, and else as
<hidden, old> in the first version and <hidden, new> in the second. So a
changed password shows, but not which hidden values are equal. As root,
--show-secrets shows them as stored.`,
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
			// Each side by its own path. A value is marked old or new
			// when the other side does not hide it, so a changed secret
			// shows; equal values on two lines are not told apart.
			aHide, bHide := redact.Sides(hiddenValues(a.Path, aData), hiddenValues(bPath, bData))
			aShown, na := hideSecrets(a.Path, aData, aHide)
			bShown, nb := hideSecrets(bPath, bData, bHide)
			d, err := store.UnifiedDiff(aShown, bShown, aLabel, bLabel)
			if err != nil {
				return fmt.Errorf("diff: %w", err)
			}
			out := cmd.OutOrStdout()
			switch {
			case d == "" && !bytes.Equal(aData, bData):
				// Hidden values that only moved (two accounts that
				// swapped hashes): each is in both versions, so each
				// shows as <hidden>.
				fmt.Fprintln(out, "no differences shown: the two differ only where values are hidden")
			case d == "":
				fmt.Fprintln(out, "no differences")
			default:
				if _, err := io.WriteString(out, d); err != nil {
					return err
				}
			}
			// One note per path that hid anything, on either side.
			if na > 0 || (nb > 0 && bPath == a.Path) {
				hiddenNote(a.Path, diffMarkers)
			}
			if nb > 0 && bPath != a.Path {
				hiddenNote(bPath, diffMarkers)
			}
			return nil
		},
	}
	addShowSecrets(cmd)
	return cmd
}

// diffMarkers is how hiddenNote names sc diff's markers; its help says
// which is which.
const diffMarkers = redact.Marker + ", " + redact.Old + " or " + redact.New

// hiddenValues returns the values hideSecrets hides in data, p's content,
// as they stand: none for a file that is not secret, or with
// --show-secrets.
func hiddenValues(p string, data []byte) []string {
	var vs []string
	hideSecrets(p, data, func(s string) string {
		vs = append(vs, s)
		return redact.Marker
	})
	return vs
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

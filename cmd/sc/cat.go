package main

import (
	"github.com/spf13/cobra"

	"smartconfig/internal/redact"
)

func newCatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cat <id>",
		Short: "Print a snapshot's content, secret values hidden",
		Long: `Print a snapshot's content exactly, except in a secret file (sc scope
says which): its secret values are shown as <hidden>. As root,
--show-secrets shows them as stored.

So do not put a secret file back with sc cat ID > FILE, in the rescue
shell or anywhere: the file would hold <hidden> in place of each
password or key. sc restore ID writes the real content (or, as root,
sc cat --show-secrets ID prints it).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			c, err := s.Get(args[0])
			if err != nil {
				return err
			}
			data, err := rowContent(s, c)
			if err != nil {
				return err
			}
			data, n := hideSecrets(c.Path, data, redact.Plain)
			if _, err := cmd.OutOrStdout().Write(data); err != nil {
				return err
			}
			if n > 0 {
				hiddenNote(c.Path, redact.Marker)
			}
			return nil
		},
	}
	addShowSecrets(cmd)
	return cmd
}

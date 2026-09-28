package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"smartconfig/internal/store"
)

func newSnapshotCmd() *cobra.Command {
	var intent string
	var quiet bool
	cmd := &cobra.Command{
		Use:   "snapshot <path>",
		Short: "Record the current content of a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mutating.Store(true)
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			c, unchanged, err := s.Snapshot(args[0], store.OriginManual, intent)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case quiet:
				fmt.Fprintln(out, c.ID)
			case unchanged:
				fmt.Fprintf(out, "unchanged since %s\n", c.ID)
			default:
				fmt.Fprintf(out, "snapshot %s  %s  (%d bytes)\n", c.ID, c.Path, c.Size)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&intent, "message", "m", "", "why the snapshot was taken")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print only the snapshot id")
	return cmd
}

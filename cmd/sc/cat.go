package main

import (
	"github.com/spf13/cobra"
)

func newCatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cat <id>",
		Short: "Print a snapshot's content exactly",
		Args:  cobra.ExactArgs(1),
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
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

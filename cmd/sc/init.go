package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"smartconfig/internal/store"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the data directory ($SC_HOME, default " + store.DefaultHome + ")",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := filepath.Abs(store.Home())
			if err != nil {
				return fmt.Errorf("resolve data directory: %w", err)
			}
			if err := store.Init(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialised %s\n", dir)
			return nil
		},
	}
}

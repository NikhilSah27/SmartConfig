package main

import (
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"smartconfig/internal/store"
)

func newLogCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "log [path]",
		Short: "List recorded changes, newest first",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			cs, err := s.List(path, n)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cs) == 0 {
				fmt.Fprintln(out, "no snapshots")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tWHEN\tORIGIN\tFILE\tSIZE\tWHAT")
			for _, c := range cs {
				size := strconv.FormatInt(c.Size, 10)
				if c.Kind != store.KindFile {
					size = c.Kind // link, deleted or digest
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID,
					time.Unix(c.TS, 0).Local().Format("2006-01-02 15:04"),
					c.Origin, c.Path, size, c.Intent)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVarP(&n, "n", "n", 50, "maximum number of rows (0 for all)")
	return cmd
}

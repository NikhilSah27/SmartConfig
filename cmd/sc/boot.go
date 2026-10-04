package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"smartconfig/internal/boot"
	"smartconfig/internal/check"
	"smartconfig/internal/store"
)

// bootRunner runs systemctl for sc boot verdict, as the M3 runner runs a
// validator: from fixed directories, no shell, LC_ALL=C, a timeout. Tests
// point it at a fake.
var bootRunner = check.Runner{Timeout: 30 * time.Second}

// newBootCmd is sc boot, run by two units at every boot (M4 plan 3.2),
// not by people: hidden.
func newBootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "boot",
		Short:  "Record this boot and its verdict (run by sc-boot-seen and sc-boot-ok)",
		Hidden: true,
		Args:   cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "seen",
		Short: "Record that this boot reached the point where /var is writable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := boot.CurrentID()
			if err != nil {
				return err
			}
			home := store.Home()
			if err := os.MkdirAll(home, 0o700); err != nil {
				return err
			}
			return boot.Seen(home, id, time.Now())
		},
	}, &cobra.Command{
		Use:   "verdict",
		Short: "Record whether this boot came up healthy (after multi-user.target)",
		Args:  cobra.NoArgs,
		RunE:  runBootVerdict,
	})
	return cmd
}

// runBootVerdict asks systemd for the states the verdict rests on, and
// records it with the newest store row: what changed after that row
// changed after this boot came up.
func runBootVerdict(cmd *cobra.Command, args []string) error {
	id, err := boot.CurrentID()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	states, err := systemctl(ctx, "is-active", "local-fs.target", "emergency.target", "rescue.target")
	if err != nil {
		return err
	}
	if len(states) != 3 {
		return fmt.Errorf("systemctl is-active gave %d states, not 3: %q", len(states), states)
	}
	failed := -1 // unknown
	if units, err := systemctl(ctx, "list-units", "--state=failed", "--no-legend", "--plain"); err == nil {
		failed = len(units)
	}
	verdict, why := boot.Judge(states[0], states[1], states[2], failed)
	// The newest row, the line between what this boot came up with and
	// what changed after; -1 when the store cannot be read (sc status then
	// looks further back), never 0, which would mean "every row".
	row := int64(-1)
	if s, err := openStore(); err == nil {
		if n, err := s.NewestRowID(); err == nil {
			row = n
		}
		s.Close()
	}
	if err := os.MkdirAll(store.Home(), 0o700); err != nil {
		return err
	}
	if err := boot.Record(store.Home(), id, verdict, time.Now(), row, why); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "boot %s: %s (%s)\n", id, verdict, why)
	return nil
}

// systemctl runs systemctl with args and returns its stdout's lines.
// is-active may exit 3 for an inactive unit and still prints every
// state; any other command must exit 0.
func systemctl(ctx context.Context, args ...string) ([]string, error) {
	res, err := bootRunner.Run(ctx, "/", "systemctl", args...)
	switch {
	case err != nil:
		return nil, err
	case !res.Found:
		return nil, fmt.Errorf("systemctl not found")
	case res.TimedOut || res.Exit < 0:
		return nil, fmt.Errorf("systemctl %s did not finish", args[0])
	case res.Exit != 0 && args[0] != "is-active":
		return nil, fmt.Errorf("systemctl %s: exit %d", args[0], res.Exit)
	}
	var lines []string
	for _, l := range strings.Split(string(res.Out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

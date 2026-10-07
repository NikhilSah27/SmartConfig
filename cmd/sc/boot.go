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

// grubenvPath is GRUB's environment block, where 42_smartconfig's code in
// grub.cfg reads smartconfig_pending. Tests point it elsewhere.
var grubenvPath = "/boot/grub/grubenv"

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
			// A boot that has its verdict is not seen again: systemctl
			// rescue or emergency, or an isolate, can start the finished
			// unit once more, and its flag would bring the menu back after
			// a healthy boot (the M4 final review, A3).
			if bs, err := boot.Read(home); err == nil {
				for _, b := range bs {
					if b.ID == id && b.Verdict != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "boot %s: already %s, not seen again\n", id, b.Verdict)
						return nil
					}
				}
			}
			// Until a healthy verdict unsets it, the next boot shows the
			// menu with "SmartConfig rescue" (plan 3.3). The flag first: a
			// "seen" line that cannot be written (a full /var) must not
			// cost the menu too (the M4 final review, C7).
			if note := menuFlag(cmd.Context(), true); note != "" {
				fmt.Fprintln(cmd.OutOrStdout(), note)
			}
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
	// ok clears the flag. bad sets it again, in case seen could not (its
	// unit failed on a /var that did not mount, or grub-editenv did):
	// the menu after a failed boot (the M4 final review, A6).
	if note := menuFlag(ctx, verdict == "bad"); note != "" {
		fmt.Fprintln(cmd.OutOrStdout(), note)
	}
	return nil
}

// menuFlag sets or unsets smartconfig_pending in grubenv with
// grub-editenv, through the runner. A grubenv that is not GRUB's
// 1024-byte block (none, or a separate /boot not mounted yet, where the
// mount point is an empty directory) is left alone. It returns what went
// wrong, for the journal, or "".
func menuFlag(ctx context.Context, set bool) string {
	fi, err := os.Lstat(grubenvPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != 1024 {
		return "no GRUB environment block at " + grubenvPath + ": the menu does not come back by itself after a failed boot"
	}
	args := []string{grubenvPath, "unset", "smartconfig_pending"}
	if set {
		args = []string{grubenvPath, "set", "smartconfig_pending=1"}
	}
	res, err := bootRunner.Run(ctx, "/", "grub-editenv", args...)
	switch {
	case err != nil:
		return err.Error()
	case !res.Found:
		return "grub-editenv not found: the menu does not come back by itself after a failed boot"
	case res.TimedOut || res.Exit != 0:
		return fmt.Sprintf("grub-editenv %s failed (exit %d): the menu flag is not as it should be", args[1], res.Exit)
	}
	return ""
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

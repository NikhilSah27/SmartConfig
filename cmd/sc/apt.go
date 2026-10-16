package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"smartconfig/internal/aptrun"
	"smartconfig/internal/fsutil"
)

// aptRunFile is where sc apt begin marks a dpkg run and scd looks for it;
// the tests point it elsewhere.
var aptRunFile = aptrun.File

// aptRunner finds the apt above sc apt begin; the tests point it elsewhere.
var aptRunner = aptrun.Runner

// newAptCmd is sc apt begin and end, which the package's apt hook
// (/etc/apt/apt.conf.d/50smartconfig) runs around each dpkg run, so scd
// tags the changes it sees meanwhile with origin apt (M5 follow-ups, step
// 8). Neither opens the store or waits for anything: apt waits for them.
func newAptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apt begin|end",
		Short: "Mark a dpkg run, so scd tags its changes (apt's hook runs it)",
		Long: `Mark a dpkg run: sc apt begin before dpkg runs, sc apt end after it. apt
runs both, from /etc/apt/apt.conf.d/50smartconfig. While the run's apt is
alive, scd records the changes it sees with origin apt, and the run in
their intent (sc log). A mark apt left behind (apt killed) is stale and
tags nothing. Root only.`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"begin", "end"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if geteuid() != 0 {
				return errors.New("sc apt needs root")
			}
			switch args[0] {
			case "begin":
				return aptBegin()
			case "end":
				if err := os.Remove(aptRunFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				return nil
			}
			return fmt.Errorf("sc apt takes begin or end, not %q", args[0])
		},
	}
}

// aptBegin marks the run of the apt above sc: apt runs its hook's lines
// with sh -c, so sc's parent is a shell.
func aptBegin() error {
	pid, start, err := aptRunner(os.Getppid())
	if err != nil {
		return fmt.Errorf("sc apt begin: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(aptRunFile), 0o755); err != nil {
		return err
	}
	m := aptrun.Mark{PID: pid, Start: start, Begun: time.Now()}
	return fsutil.WriteAtomic(aptRunFile, m.Encode(), 0o644, os.Getuid(), os.Getgid())
}

// aptRunFor is the mark scd reads: aptRunFile for the system's scd, none
// for one that watches --root directories.
func aptRunFor(roots []string) string {
	if len(roots) > 0 {
		return ""
	}
	return aptRunFile
}

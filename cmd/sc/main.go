// Command sc snapshots, diffs and restores config files.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

func main() {
	// On Ctrl-C or SIGTERM, remove temp files that were prepared but not yet
	// renamed into place (deferred cleanups do not run on a signal), then
	// exit with one line. The database rolls back any open transaction by
	// itself the next time it is opened.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		fsutil.RemovePending()
		fmt.Fprintf(os.Stderr, "sc: interrupted by %s\n", sig)
		os.Exit(1)
	}()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI and returns the exit code. Any error, including a
// panic, becomes one line on stderr and exit code 1.
func run(args []string, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "sc: internal error: %v\n", r)
			code = 1
		}
	}()
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		msg := strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
		fmt.Fprintf(stderr, "sc: %s\n", msg)
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "sc",
		Short:         "Snapshot, diff and restore config files",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		newInitCmd(),
		newSnapshotCmd(),
		newLogCmd(),
		newCatCmd(),
		newDiffCmd(),
		newRestoreCmd(),
	)
	return root
}

func openStore() (*store.Store, error) {
	return store.Open(store.Home())
}

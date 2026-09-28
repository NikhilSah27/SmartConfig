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
	sigs := make(chan os.Signal, 2)
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		// A signal the caller chose to ignore (nohup, a script's trap)
		// stays ignored.
		if !signal.Ignored(sig) {
			signal.Notify(sigs, sig)
		}
	}
	go handleSignals(sigs)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// handleSignals handles Ctrl-C, SIGTERM, SIGHUP and SIGQUIT. If a snapshot
// or restore is running, the first signal asks it to stop at its next safe
// point; run then reports what happened through the normal error path and
// exits 1, so deferred cleanups run and the message says whether the file
// changed. With nothing running, or on a second signal, sc stops at once,
// first removing temp files it prepared but did not rename. Deferred
// cleanups do not run on that path, and SQLite rolls back an open
// transaction the next time the database is opened.
func handleSignals(sigs <-chan os.Signal) {
	sig := <-sigs
	store.Interrupt()
	if store.Busy() {
		sig = <-sigs
	}
	fsutil.RemovePending()
	fmt.Fprintf(os.Stderr, "sc: interrupted by %s\n", sig)
	os.Exit(1)
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

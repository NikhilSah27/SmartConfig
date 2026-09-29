// Command sc snapshots, diffs and restores config files.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// stopSignals are the signals sc handles. SIGINT, SIGTERM and SIGHUP end sc
// by the same signal after cleanup; the others (whose default action would
// print a Go stack trace or dump core) end it with exit status 1.
var stopSignals = []os.Signal{
	os.Interrupt, syscall.SIGTERM, syscall.SIGHUP,
	syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGTRAP, syscall.SIGSYS,
	syscall.SIGILL, syscall.SIGBUS, syscall.SIGFPE, syscall.SIGSEGV,
}

// mutating is set for the whole of a snapshot or restore command, including
// printing its result, so a signal never cuts that result short.
var mutating atomic.Bool

// received is the number of the first stop signal, or 0.
var received atomic.Int32

// The command and a second stop signal can race to say how sc ended. The
// first to claim the end prints its line and the other stays silent, so sc
// prints one line. A command that succeeded claims it too, so no line after
// its result can contradict it.
var (
	endMu sync.Mutex
	ended bool
)

// claimEnd reports whether the caller is the first to say how sc ended.
func claimEnd() bool {
	endMu.Lock()
	defer endMu.Unlock()
	first := !ended
	ended = true
	return first
}

// testHookAfterRun runs in main after the command has returned; the signal
// tests' build (-tags sctest) sets it.
var testHookAfterRun = func() {}

func main() {
	sigs := make(chan os.Signal, len(stopSignals))
	for _, sig := range stopSignals {
		// A signal the caller chose to ignore (nohup, a script's trap)
		// stays ignored.
		if !signal.Ignored(sig) {
			signal.Notify(sigs, sig)
		}
	}
	go handleSignals(sigs)
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	testHookAfterRun()
	if n := received.Load(); n != 0 {
		endBy(syscall.Signal(n))
	}
	os.Exit(code)
}

// handleSignals implements sc's contract for stop signals:
//   - During a snapshot or restore, the first signal asks it to stop at its
//     next safe point. The command then reports what happened through its
//     normal error or success line (so deferred cleanups run and the line
//     says whether the file changed), and main ends by the signal.
//   - A second signal stops at once, unless a restore may already have
//     renamed its file: then sc keeps waiting for that restore to report.
//     After the first signal it makes at most one more COMMIT attempt, so
//     the wait is bounded.
//   - Any other command (log, cat, diff, init) stops at once.
//
// Stopping at once removes the temp files sc prepared but did not rename and
// prints one line; SQLite rolls back an open transaction at the next open.
func handleSignals(sigs <-chan os.Signal) {
	sig := <-sigs
	received.Store(int32(sig.(syscall.Signal)))
	store.Interrupt()
	if !mutating.Load() {
		fsutil.RemovePending()
		if claimEnd() {
			fmt.Fprintf(os.Stderr, "sc: interrupted by %s\n", sig)
		}
		endBy(sig)
	}
	for {
		sig = <-sigs
		if store.ForceStop() {
			break
		}
	}
	fsutil.RemovePending()
	if claimEnd() {
		fmt.Fprintf(os.Stderr, "sc: stopped by %s (file not changed)\n", sig)
	}
	endBy(sig)
}

// endBy ends sc after a stop signal. For SIGINT, SIGTERM and SIGHUP it dies
// by that signal, as programs are expected to, so a calling shell sees an
// interrupted command (a loop stops instead of running the next step). The
// other signals end with exit status 1.
func endBy(sig os.Signal) {
	switch sig {
	case os.Interrupt, syscall.SIGTERM, syscall.SIGHUP:
		signal.Reset(sig)
		syscall.Kill(syscall.Getpid(), sig.(syscall.Signal))
		time.Sleep(time.Second) // the signal ends the process; this is a fallback
	}
	os.Exit(1)
}

// run executes the CLI and returns the exit code. Any error, including a
// panic, becomes one line on stderr and exit code 1.
func run(args []string, stdout, stderr io.Writer) (code int) {
	endMu.Lock()
	ended = false // the tests call run many times in one process
	endMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			if claimEnd() {
				fmt.Fprintf(stderr, "sc: internal error: %v\n", r)
			}
			code = 1
		}
	}()
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		if claimEnd() {
			msg := strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
			fmt.Fprintf(stderr, "sc: %s\n", msg)
		}
		return 1
	}
	claimEnd()
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

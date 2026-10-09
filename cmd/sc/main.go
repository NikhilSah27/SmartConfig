// Command sc snapshots, diffs and restores config files.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"smartconfig/internal/check"
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

// editing is set while sc edit's editor runs.
var editing atomic.Bool

// ignoreHangup is set by sc status --console. The console's getty can hang
// it up while sc runs (in the M4 lab, serial-getty@ttyS0 starting next to
// emergency.service): the report goes to consoles sc opens itself, so a
// hangup must not end sc before it is written. Caught and dropped, not
// SIG_IGN, which the commands sc runs would inherit.
var ignoreHangup atomic.Bool

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

// watchCancel holds the cancel function of a running sc watch: a stop
// signal ends the watch cleanly instead of killing sc (plan 8: exit 0).
var watchCancel atomic.Pointer[context.CancelFunc]

// watchRescan holds the running watcher's RequestRescan, for SIGHUP.
var watchRescan atomic.Pointer[func()]

// watchStopped is set when a signal ended sc watch through watchCancel.
var watchStopped atomic.Bool

// testHookAfterRun runs in main after the command has returned,
// testHookInStatus in sc status once it has set itself up, and
// testHookStatusChecks once it has its store and its checks' scratch;
// the signal tests' build (-tags sctest) sets them.
var (
	testHookAfterRun     = func() {}
	testHookInStatus     = func() {}
	testHookStatusChecks = func() {}
)

// consoleStatus reports whether args are sc status --console, as the
// rescue drop-in runs it.
func consoleStatus(args []string) bool {
	if len(args) == 0 || args[0] != "status" {
		return false
	}
	for _, a := range args[1:] {
		if a == "--console" || a == "--console=true" {
			return true
		}
	}
	return false
}

func main() {
	// Before the handlers are there: a hangup caught while cobra is still
	// on its way to sc status would end sc (the chunk D review measured
	// that window at 3 to 5 ms). What comes before signal.Notify below is
	// the kernel's default and cannot be closed from in here.
	if consoleStatus(os.Args[1:]) {
		ignoreHangup.Store(true)
	}
	sigs := make(chan os.Signal, len(stopSignals))
	for _, sig := range stopSignals {
		// A signal the caller chose to ignore (nohup, a script's trap)
		// stays ignored.
		if !signal.Ignored(sig) {
			signal.Notify(sigs, sig)
		}
	}
	go handleSignals(sigs)
	// A reader that goes away (sc log | head): with SIGPIPE caught, the
	// write fails with EPIPE instead of Go's death by SIGPIPE, so the
	// command returns and its deferred cleanups remove sc's private
	// copies (a repaired store, scratch files). sc then exits 1 without
	// a word, as when its caller ignores SIGPIPE. sc watch ignores it.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	testHookAfterRun()
	if n := received.Load(); n != 0 && !watchStopped.Load() {
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
	for {
		switch {
		case sig == syscall.SIGHUP && watchRescan.Load() != nil:
			// sc watch: SIGHUP asks for a rescan (systemctl reload scd),
			// as daemons treat it; it never stops the watcher.
			(*watchRescan.Load())()
		case sig == syscall.SIGHUP && ignoreHangup.Load():
			// sc status --console: the report is still to be written.
		case editing.Load() && (sig == os.Interrupt || sig == syscall.SIGQUIT):
			// sc edit while its editor runs: Ctrl-C and Ctrl-\ go to the
			// whole foreground group and are the editor's to handle, as
			// with sudoedit and git. sc waits for the editor.
		default:
			goto stop
		}
		sig = <-sigs
	}
stop:
	received.Store(int32(sig.(syscall.Signal)))
	if c := watchCancel.Load(); c != nil && isStop(sig) {
		// sc watch: stop watching, finish the batch, exit 0. A second
		// stop signal stops at once.
		watchStopped.Store(true)
		(*c)()
		for sig = <-sigs; sig == syscall.SIGHUP; sig = <-sigs {
		}
		check.Stop()
		if claimEnd() {
			fmt.Fprintf(os.Stderr, "%ssc: stopped by %s\n", errPrefix(os.Stderr), sig)
		}
		os.Exit(1)
	}
	store.Interrupt()
	if !mutating.Load() {
		check.Stop()
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
	check.Stop()
	fsutil.RemovePending()
	if claimEnd() {
		fmt.Fprintf(os.Stderr, "sc: stopped by %s (file not changed)\n", sig)
	}
	endBy(sig)
}

// errPrefix is "<3>" when stderr is journald (JOURNAL_STREAM names it), so
// a fatal error of scd is logged at err priority and shows up in
// journalctl -p warning (plan 7.6); "" otherwise.
func errPrefix(stderr io.Writer) string {
	if f, ok := stderr.(*os.File); ok && isJournal(f, os.Getenv("JOURNAL_STREAM")) {
		return "<3>"
	}
	return ""
}

// isStop reports whether sig asks sc to stop (not a fault signal).
func isStop(sig os.Signal) bool {
	return sig == os.Interrupt || sig == syscall.SIGTERM || sig == syscall.SIGHUP
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
func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}

// runContext is run with a context: cancelling it stops sc watch (tests).
func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) (code int) {
	endMu.Lock()
	ended = false // the tests call run many times in one process
	endMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			if claimEnd() {
				fmt.Fprintf(stderr, "%ssc: internal error: %v\n", errPrefix(stderr), r)
			}
			code = 1
		}
	}()
	noteOut = stderr
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		var ec exitCode
		if errors.As(err, &ec) {
			claimEnd()
			return int(ec)
		}
		// Stopped by a signal: its handler says so and ends sc.
		if errors.Is(err, check.ErrStopped) {
			return 1
		}
		// A reader that went away is no error to report (main).
		if claimEnd() && !errors.Is(err, syscall.EPIPE) {
			msg := strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
			fmt.Fprintf(stderr, "%ssc: %s\n", errPrefix(stderr), msg)
		}
		return 1
	}
	claimEnd()
	return 0
}

// exitCode is the error of a command that has printed what it found and
// only has an exit status left to give (sc check: 2 when a file has a
// blocker or an error).
type exitCode int

func (e exitCode) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// newHelpCmd is cobra's help command, but an unknown topic is an error,
// one line and exit 1, as an unknown command is (M5 follow-up 5: cobra's
// printed the usage and exited 0).
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(c *cobra.Command, args []string) error {
			cmd, rest, err := c.Root().Find(args)
			if err != nil || cmd == nil || len(rest) > 0 {
				return fmt.Errorf("unknown command %q for \"sc help\"", strings.Join(args, " "))
			}
			cmd.InitDefaultHelpFlag()
			return cmd.Help()
		},
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "sc",
		Short:         "Snapshot, diff and restore config files",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetHelpCommand(newHelpCmd())
	root.AddCommand(
		newInitCmd(),
		newSnapshotCmd(),
		newLogCmd(),
		newCatCmd(),
		newDiffCmd(),
		newRestoreCmd(),
		newWatchCmd(),
		newCheckCmd(),
		newEditCmd(),
		newScopeCmd(),
		newStatusCmd(),
		newBootCmd(),
		newVersionCmd(),
		newPruneCmd(),
	)
	return root
}

func openStore() (*store.Store, error) {
	s, err := store.Open(store.Home())
	if err == nil && s.Copied() {
		// A crash left a write unfinished where sc may not write (the
		// rescue shell's read-only root): what sc shows is the store as
		// of before that write.
		fmt.Fprintln(noteOut, "sc: note: the store has a write a crash left unfinished; sc reads a repaired copy, and the store itself is repaired by the next sc run on a writable root")
	}
	if err == nil && testHookOpenStore != nil {
		testHookOpenStore(s)
	}
	return s, err
}

// noteOut is where a command's one-line notes go: the run's stderr.
var noteOut io.Writer = os.Stderr

// testHookOpenStore, if set by a test, runs on every store a command opens
// (to set the fingerprint rule, say).
var testHookOpenStore func(*store.Store)

// rowContent returns what a row shows as text: a file's content, or a
// link's target on one line. A fingerprint-only path, a deletion and a
// digest row have none, and each says why in one line.
func rowContent(s *store.Store, c store.Change) ([]byte, error) {
	if s.FingerprintOnly(c.Path) {
		return nil, fmt.Errorf("%s is fingerprint-only; sc never shows its content", c.Path)
	}
	switch c.Kind {
	case store.KindLink:
		return []byte(c.Target + "\n"), nil
	case store.KindDeleted:
		return nil, fmt.Errorf("%s records that %s %s", c.ID, c.Path, absence(c))
	case store.KindDigest:
		return nil, fmt.Errorf("%s keeps only a fingerprint of %s, not its content", c.ID, c.Path)
	}
	return s.Blob(c.Blob)
}

// show returns s as it is, or quoted like a Go string when it holds a
// control character or invalid UTF-8, so a planted file name or link
// target cannot forge output lines.
func show(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strconv.Quote(s)
}

// absence says how a deleted row came about.
func absence(c store.Change) string {
	if strings.HasPrefix(c.Intent, "did not exist") {
		return "did not exist"
	}
	return "was deleted"
}

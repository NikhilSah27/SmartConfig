package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"smartconfig/internal/store"
	"smartconfig/internal/watch"
)

func newWatchCmd() *cobra.Command {
	var roots []string
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch /etc, /boot/grub and each login's ~/.ssh and record every change",
		Long: `Watch /etc, /boot/grub and each login account's ~/.ssh and record
every change in $SC_HOME, which is created if missing. Runs until SIGINT
or SIGTERM; scd.service runs it. Needs root unless --root is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(roots) == 0 && os.Geteuid() != 0 {
				return errors.New("sc watch needs root (or --root DIR to watch a directory of your own)")
			}
			var abs []string
			for _, r := range roots {
				a, err := filepath.Abs(r)
				if err != nil {
					return fmt.Errorf("--root %s: %w", r, err)
				}
				abs = append(abs, a)
			}
			w, err := watch.New(watch.Config{
				Home:  store.Home(),
				Roots: abs,
				Log:   newLogWriter(os.Stderr, cmd.ErrOrStderr()),
			})
			if err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			watchCancel.Store(&cancel)
			defer watchCancel.Store(nil)
			return w.Run(ctx)
		},
	}
	cmd.Flags().StringArrayVar(&roots, "root", nil, "watch DIR instead of the default roots (repeatable); the default rules still apply")
	return cmd
}

// logWriter turns the watcher's "<N>" priority prefix (plan 7.6) into what
// stderr wants: kept for journald, which parses it, and replaced by the
// time of day in a terminal. stderr is journald only when JOURNAL_STREAM
// names its device and inode (systemd.exec(5)).
type logWriter struct {
	mu      sync.Mutex
	out     io.Writer
	journal bool
	now     func() time.Time
}

// newLogWriter writes to out; f is the stderr file it checks against
// JOURNAL_STREAM (out and f are the same in production).
func newLogWriter(f *os.File, out io.Writer) *logWriter {
	return &logWriter{out: out, journal: isJournal(f, os.Getenv("JOURNAL_STREAM")), now: time.Now}
}

// isJournal reports whether f is the stream JOURNAL_STREAM ("dev:ino")
// describes.
func isJournal(f *os.File, env string) bool {
	dev, ino, ok := strings.Cut(env, ":")
	if !ok || f == nil {
		return false
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return false
	}
	return strconv.FormatUint(uint64(st.Dev), 10) == dev && strconv.FormatUint(st.Ino, 10) == ino
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.journal {
		return l.out.Write(p)
	}
	var b bytes.Buffer
	for _, line := range bytes.SplitAfter(p, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if len(line) > 3 && line[0] == '<' && line[2] == '>' && line[1] >= '0' && line[1] <= '7' {
			line = line[3:]
		}
		b.WriteString(l.now().Format("15:04:05 "))
		b.Write(line)
	}
	if _, err := l.out.Write(b.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}

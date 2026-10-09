package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"smartconfig/internal/boot"
	"smartconfig/internal/store"
)

// pruneStdin is where sc prune reads its answer from; the tests replace it.
var pruneStdin io.Reader = os.Stdin

// pruneNow is the clock sc prune counts --older-than from; the tests
// replace it.
var pruneNow = time.Now

func newPruneCmd() *cobra.Command {
	var olderThan string
	var yes bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete old versions scd recorded, and the stored content nothing uses",
		Long: `Delete the versions scd recorded that a newer version replaced more than
--older-than ago (90 days by default), and then the stored content that
no row uses any more.

Kept whatever their age: each file's newest and first rows; each file's
version at every boot the boots file records, which is what sc status
compares with; and every manual, sc edit, restore and pre-restore row.

sc prune says what it would delete and asks first; --yes does not ask.
It deletes nothing until a healthy boot is recorded, nor while scd still
runs a sc that an upgrade replaced. Nothing is ever deleted unless
sc prune is run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			age, err := parseAge(olderThan)
			if err != nil {
				return err
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			// The versions sc status compares with are those at the boots
			// the boots file records; until one is a healthy boot with a
			// row, sc status compares with rows sc prune would delete (the
			// chunk I review, I2).
			boots, err := boot.Read(store.Home())
			if err != nil {
				return fmt.Errorf("%w; nothing pruned", err)
			}
			var lines []int64
			healthy := false
			for _, b := range boots {
				lines = append(lines, b.RowID)
				healthy = healthy || b.Verdict == "ok" && b.RowID >= 0
			}
			if !healthy {
				return errors.New("no healthy boot is recorded yet (sc status lists the boots); nothing pruned")
			}
			// A writer of a release before this one does not put its blob
			// again under the lock (I4).
			if pid := scdReplaced(); pid != "" {
				return fmt.Errorf("scd (pid %s) still runs the sc an upgrade replaced: run sudo systemctl restart scd, then this again; nothing pruned", pid)
			}
			cutoff := pruneNow().Add(-age)
			when := cutoff.Local().Format("2006-01-02 15:04")
			out := cmd.OutOrStdout()
			plan, err := s.Prune(cutoff, lines, true)
			if err != nil {
				return err
			}
			if plan.Rows == 0 && plan.Blobs == 0 {
				fmt.Fprintf(out, "nothing to prune: no version scd recorded was replaced before %s,\n", when)
				fmt.Fprintln(out, "and no stored content is unused")
				return nil
			}
			fmt.Fprintf(out, "%s recorded by scd and replaced before %s can go,\n", count(plan.Rows, "version"), when)
			fmt.Fprintf(out, "and %s of stored content no row will use any more.\n", size(plan.Bytes))
			fmt.Fprintln(out, "Kept: each file's newest and first rows, its version at every recorded")
			fmt.Fprintln(out, "boot, and every manual, sc edit, restore and pre-restore row.")
			if !yes {
				fmt.Fprint(out, "Delete them? [y/N]: ")
				answer, _ := bufio.NewReader(pruneStdin).ReadString('\n')
				switch strings.ToLower(strings.TrimSpace(answer)) {
				case "y", "yes":
				default:
					if !strings.HasSuffix(answer, "\n") {
						fmt.Fprintln(out) // end of input: the prompt's line ends here
					}
					fmt.Fprintln(out, "nothing deleted")
					return nil
				}
			}
			// Only the deleting is a mutating command: a signal at the
			// prompt ends sc at once; one now stops Prune between batches,
			// and it says what it did (I6).
			mutating.Store(true)
			done, err := s.Prune(cutoff, lines, false)
			fmt.Fprintf(out, "deleted %s and %s of stored content\n", count(done.Rows, "version"), size(done.Bytes))
			if done.Skipped > 0 {
				fmt.Fprintf(out, "%s could not be removed (%v)\n", count(done.Skipped, "stored file"), done.SkipErr)
			}
			if errors.Is(err, store.ErrInterrupted) {
				return errors.New("stopped by a signal; sc prune deletes the rest the next time")
			}
			return err
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "90d", `how long ago a version was replaced: days ("90d") or hours ("36h")`)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask")
	return cmd
}

// parseAge reads --older-than: a whole number of days ("90d") or hours
// ("36h"), from one hour to 100 years. An age that overflowed would put the
// cutoff in the future, and every old enough row would go.
func parseAge(s string) (time.Duration, error) {
	units := map[byte]time.Duration{'d': 24 * time.Hour, 'h': time.Hour}
	if len(s) >= 2 && s[0] >= '0' && s[0] <= '9' {
		if u, ok := units[s[len(s)-1]]; ok {
			// n is checked before it is multiplied: a product that
			// overflowed could pass any check made after.
			n, err := strconv.Atoi(s[:len(s)-1])
			if err == nil && n >= 1 && n <= int(100*365*24*time.Hour/u) {
				return time.Duration(n) * u, nil
			}
		}
	}
	return 0, fmt.Errorf("--older-than %q: give days or hours, like 90d or 36h (at most 100 years)", s)
}

// size is n bytes for people: bytes, KiB, then MiB with one decimal.
func size(n int64) string {
	switch {
	case n < 1<<10:
		return count(int(n), "byte")
	case n < 1<<20:
		return fmt.Sprintf("%d KiB", (n+1<<9)>>10)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// scdReplaced returns the pid of a running scd (sc watch) whose program an
// upgrade replaced on disk, "" if none: /proc shows its exe as deleted.
func scdReplaced() string {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
		if len(args) != 2 || filepath.Base(args[0]) != "sc" || args[1] != "watch" {
			continue
		}
		if exe, err := os.Readlink(filepath.Join(procDir, e.Name(), "exe")); err == nil && strings.HasSuffix(exe, " (deleted)") {
			return e.Name()
		}
	}
	return ""
}

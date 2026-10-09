package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
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
		Short: "Delete old automatic rows and the blobs nothing uses any more",
		Long: `Delete the rows scd recorded more than --older-than ago (90 days by
default), and then the stored contents that no row uses any more.

Kept whatever their age: each file's newest row; each file's version at
every boot the boots file records, which is what sc status compares with;
and every manual, sc edit, restore and pre-restore row.

sc prune says what it would delete and asks first; --yes does not ask.
Nothing is ever deleted unless sc prune is run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			age, err := parseAge(olderThan)
			if err != nil {
				return err
			}
			mutating.Store(true)
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			// Without the boots file, sc prune cannot know which versions
			// sc status compares with: it deletes nothing.
			boots, err := boot.Read(store.Home())
			if err != nil {
				return fmt.Errorf("%w; nothing pruned", err)
			}
			var lines []int64
			for _, b := range boots {
				lines = append(lines, b.RowID)
			}
			cutoff := pruneNow().Add(-age)
			out := cmd.OutOrStdout()
			plan, err := s.Prune(cutoff, lines, true)
			if err != nil {
				return err
			}
			if plan == (store.PruneResult{}) {
				fmt.Fprintf(out, "nothing to prune: no automatic row from before %s can go, and no stored version is unused\n", cutoff.Local().Format("2006-01-02 15:04"))
				return nil
			}
			fmt.Fprintf(out, "%s recorded by scd before %s can go,\n", count(plan.Rows, "row"), cutoff.Local().Format("2006-01-02 15:04"))
			fmt.Fprintf(out, "and %s (%s) that no row will use any more.\n", count(plan.Blobs, "stored version"), mib(plan.Bytes))
			fmt.Fprintln(out, "Kept: each file's newest row, its version at every recorded boot, and")
			fmt.Fprintln(out, "every manual, sc edit, restore and pre-restore row.")
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
			done, err := s.Prune(cutoff, lines, false)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "deleted %s and %s (%s)\n", count(done.Rows, "row"), count(done.Blobs, "stored version"), mib(done.Bytes))
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "90d", `age of the rows to delete: days ("90d") or hours ("36h")`)
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

// mib is a size in MiB with one decimal.
func mib(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

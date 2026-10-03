package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"smartconfig/internal/check"
	"smartconfig/internal/scope"
	"smartconfig/internal/store"
)

// passwdPath is where sc scope finds the login accounts whose .ssh is a
// root; the tests point it elsewhere.
var passwdPath = "/etc/passwd"

func newScopeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scope <path>...",
		Short: "Explain what SmartConfig does with a path",
		Long: `Explain what SmartConfig does with a path: whether scd records it and
which line of the built-in scope decides, how loudly a change is logged
(its tier), whether only a fingerprint is kept, which checker reads it,
and when a saved change takes effect.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sc := machineScope()
			g := check.DefaultGraph()
			out := cmd.OutOrStdout()
			for i, a := range args {
				p, err := filepath.Abs(a)
				if err != nil {
					return err
				}
				if i > 0 {
					fmt.Fprintln(out)
				}
				explainPath(out, sc.Explain(p), g)
			}
			return nil
		},
	}
}

// machineScope is the built-in scope as scd uses it on this machine: with
// each login's .ssh as a root and sc's own data directory left out. The
// .ssh roots are listed whether or not they exist.
func machineScope() *scope.Scope {
	var ssh []string
	if b, err := os.ReadFile(passwdPath); err == nil {
		for _, h := range scope.LoginHomes(b) {
			ssh = append(ssh, filepath.Join(h, ".ssh"))
		}
	}
	home, _ := filepath.Abs(store.Home())
	return scope.Default().With(home, ssh)
}

var tierNames = map[int]string{1: "boot", 2: "access", 3: "network", 4: "other"}

// explainPath prints what w and g say about one path, one property a line.
func explainPath(out io.Writer, w scope.Why, g *check.Graph) {
	// A scope line's text can be long: it goes on a line of its own.
	at := func(l scope.Line) string { return fmt.Sprintf("scope line %d:\n            %s", l.N, l.Text) }
	fmt.Fprintln(out, show(w.Path))
	switch {
	case w.Own:
		fmt.Fprintln(out, "  recorded: no, it is in sc's own data directory")
	case w.Root == "":
		fmt.Fprintf(out, "  recorded: no, it is outside every watched directory:\n            %s\n", strings.Join(machineRoots(), ", "))
	case !w.Recorded && w.At != w.Path:
		fmt.Fprintf(out, "  recorded: no, %s is left out by %s\n", show(w.At), at(w.Rule))
	case !w.Recorded:
		fmt.Fprintf(out, "  recorded: no, left out by %s\n", at(w.Rule))
	case w.Rule.N != 0:
		fmt.Fprintf(out, "  recorded: yes, under %s, kept by %s\n", w.Root, at(w.Rule))
	default:
		fmt.Fprintf(out, "  recorded: yes, under %s\n", w.Root)
	}
	if w.Recorded {
		tier := fmt.Sprintf("  tier:     %d (%s)", w.Tier, tierNames[w.Tier])
		if w.TierRule.N != 0 {
			tier += ", " + at(w.TierRule)
		}
		fmt.Fprintln(out, tier)
	}
	if w.Digest {
		fmt.Fprintf(out, "  content:  a fingerprint only, never stored or shown, %s\n", at(w.DigRule))
	}
	if c := g.Checker(w.Path); c != "" {
		fmt.Fprintf(out, "  checker:  %s (sc check %s)\n", c, show(w.Path))
		if a := g.Apply(w.Path); a != "" {
			fmt.Fprintf(out, "  applies:  %s\n", a)
		}
	} else {
		fmt.Fprintln(out, "  checker:  none")
	}
}

// machineRoots lists the directories scd watches on this machine.
func machineRoots() []string { return machineScope().Roots() }

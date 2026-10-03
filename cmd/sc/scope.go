package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
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
			m := machineScope()
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
				explainPath(out, m.Explain(p), m, g)
			}
			return nil
		},
	}
}

// machine is the built-in scope as scd uses it on this machine, with
// what lstat says of its roots.
type machine struct {
	*scope.Scope
	unused map[string]string // a root scd does not watch: why
	unseen map[string]bool   // a root sc may not look at; scd, as root, may
}

// machineScope is the scope with each login's .ssh as a root and sc's own
// data directory left out. Like scd it looks at each root with lstat.
func machineScope() machine {
	var ssh []string
	if b, err := os.ReadFile(passwdPath); err == nil {
		for _, h := range scope.LoginHomes(b) {
			ssh = append(ssh, filepath.Join(h, ".ssh"))
		}
	}
	home, _ := filepath.Abs(store.Home())
	sc := scope.Default().With(home, ssh)
	m := machine{sc, map[string]string{}, map[string]bool{}}
	for _, r := range sc.Roots() {
		fi, err := os.Lstat(r)
		switch {
		case errors.Is(err, fs.ErrPermission):
			m.unseen[r] = true
		case fsutil.IsNotExist(err):
			m.unused[r] = "it does not exist"
		case err != nil:
			m.unused[r] = fsutil.ErrText(err)
		case fi.Mode()&os.ModeSymlink != 0:
			m.unused[r] = "it is a symlink"
		case !fi.IsDir():
			m.unused[r] = "it is not a directory"
		}
	}
	return m
}

var tierNames = map[int]string{1: "boot", 2: "access", 3: "network", 4: "other"}

// explainPath prints what w, m and g say about one path, one property a
// line.
func explainPath(out io.Writer, w scope.Why, m machine, g *check.Graph) {
	// A scope line's text can be long: it goes on a line of its own.
	at := func(l scope.Line) string { return fmt.Sprintf("scope line %d:\n            %s", l.N, l.Text) }
	fmt.Fprintln(out, show(w.Path))
	switch {
	case w.Own:
		fmt.Fprintln(out, "  recorded: no, it is in sc's own data directory")
	case w.Root == "":
		fmt.Fprintf(out, "  recorded: no, it is outside every watched directory:\n            %s\n", strings.Join(m.watched(), ", "))
	case !w.Recorded && w.At != w.Path:
		fmt.Fprintf(out, "  recorded: no, %s is left out by %s\n", show(w.At), at(w.Rule))
	case !w.Recorded:
		fmt.Fprintf(out, "  recorded: no, left out by %s\n", at(w.Rule))
	case m.unused[w.Root] != "":
		// As scd: a root that is not a real directory is not watched.
		fmt.Fprintf(out, "  recorded: no, scd does not watch %s: %s\n", show(w.Root), m.unused[w.Root])
		w.Recorded = false
	case m.unseen[w.Root]:
		fmt.Fprintf(out, "  recorded: yes if scd watches %s, which sc cannot look at\n            (permission denied); run sc scope as root to be sure\n", show(w.Root))
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

// watched lists the directories scd watches on this machine, with those
// sc cannot look at.
func (m machine) watched() []string {
	var out []string
	for _, r := range m.Roots() {
		if m.unused[r] == "" {
			out = append(out, r)
		}
	}
	return out
}

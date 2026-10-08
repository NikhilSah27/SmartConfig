package main

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is the package version, set by the build (make build:
// -ldflags -X main.version=$(VERSION), from scripts/version.sh); "devel"
// for a go build of its own.
var version = "devel"

// buildRevision is the commit sc was built from, from Go's own stamp.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return revision(info.Settings)
}

// revision is the commit in a build's settings, "-dirty" after it for a
// tree with changes; "" when the build has none (a go build outside a
// checkout, or in a git worktree, which Go does not stamp).
func revision(settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" && dirty {
		rev += "-dirty"
	}
	return rev
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print sc's version and the commit it was built from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rev := buildRevision()
			if rev == "" {
				rev = "no commit stamp"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sc %s (%s, %s)\n", version, rev, runtime.Version())
			return nil
		},
	}
}

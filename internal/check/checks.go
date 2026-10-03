//go:build linux

package check

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Checks runs checkers (plan 5). The zero value plus Home works.
type Checks struct {
	Graph *Graph // nil: DefaultGraph()
	Home  string // $SC_HOME: scratch copies go under Home/tmp
	Run   Runner

	exists      func(path string) bool            // nil: the path exists here; tests fake the machine
	lstat       func(string) (os.FileInfo, error) // nil: os.Lstat; tests fake the machine
	sshdHostKey string                            // tests: a throwaway host key for sshd -t when not root
	netplanRoot string                            // "": /; tests fake the machine's {lib,etc,run}/netplan
}

// Report is the result of checking one file.
type Report struct {
	Checker  string    // "" when no checker reads the file
	Findings []Finding // sorted by line
	Notes    []string  // what could not be checked, e.g. "no validator found (findmnt)"
}

// input is what a checker gets.
type input struct {
	path string // the real path
	data []byte // the content to check
	file string // a scratch copy of data, named like path
	// saved is set for a version from the store (sc check <id>): what is
	// on disk at path now, its mode for one, is not that version's.
	saved bool
}

// checkers maps the graph's checker names to their code.
var checkers = map[string]func(context.Context, *Checks, input) ([]Finding, []string, error){
	"fstab":    checkFstab,
	"sudoers":  checkSudoers,
	"sshd":     checkSshd,
	"unit":     checkUnit,
	"shsyntax": checkShSyntax,
	"grubcfg":  checkGrubCfg,
	"nsswitch": checkNsswitch,
	"preload":  checkPreload,
	"flag":     checkFlag,
	"hosts":    checkHosts,
	"netplan":  checkNetplan,
	"udev":     checkUdev,
	"passwd":   checkPasswd,
	"group":    checkGroup,
	"sysctl":   checkSysctl,
}

// Check runs the checker the graph names for path on data, which need not
// be what is on disk: sc edit checks a candidate, sc check <id> a saved
// version. A file no checker reads gives an empty Report.
func (c *Checks) Check(ctx context.Context, path string, data []byte) (Report, error) {
	return c.check(ctx, input{path: path, data: data})
}

// CheckSaved is Check for a version from the store: rules about the file
// on disk now (sudoers-mode) are left out.
func (c *Checks) CheckSaved(ctx context.Context, path string, data []byte) (Report, error) {
	return c.check(ctx, input{path: path, data: data, saved: true})
}

func (c *Checks) check(ctx context.Context, in input) (Report, error) {
	path, data := in.path, in.data
	g := c.Graph
	if g == nil {
		g = DefaultGraph()
	}
	name := g.Checker(path)
	if name == "" {
		return Report{}, nil
	}
	fn := checkers[name]
	if fn == nil {
		return Report{}, fmt.Errorf("check %s: the graph names checker %s, which does not exist", path, name)
	}
	file, cleanup, err := Scratch(c.Home, path, data)
	if err != nil {
		return Report{}, err
	}
	defer cleanup()
	in.file = file
	fs, notes, err := fn(ctx, c, in)
	if err != nil {
		return Report{}, fmt.Errorf("check %s: %w", path, err)
	}
	for i := range fs {
		fs[i].Path = path
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Line < fs[j].Line })
	return Report{Checker: name, Findings: fs, Notes: notes}, nil
}

// pathExists reports whether p exists on this machine.
func (c *Checks) pathExists(p string) bool {
	if c.exists != nil {
		return c.exists(p)
	}
	_, err := os.Stat(p)
	return err == nil
}

// validate runs a validator and turns "not installed", "did not finish",
// "was killed" and "output cut" into notes, so a caller never takes a run
// that did not happen for a clean one. ok is false when there is no output
// to read.
func (c *Checks) validate(ctx context.Context, in input, tool string, args ...string) (res Result, notes []string, ok bool, err error) {
	res, err = c.Run.Run(ctx, filepath.Dir(in.file), tool, args...)
	switch {
	case err != nil:
		return res, nil, false, err
	case !res.Found:
		return res, []string{"no validator found (" + tool + "); only sc's own rules ran"}, false, nil
	case res.TimedOut:
		return res, []string{tool + " did not finish in time; only sc's own rules ran"}, false, nil
	case res.Exit < 0:
		return res, []string{tool + " was killed; only sc's own rules ran"}, false, nil
	case res.Truncated:
		notes = []string{tool + " printed more than sc reads; some of its findings may be missing"}
	}
	return res, notes, true, nil
}

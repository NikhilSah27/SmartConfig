//go:build linux

package check

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
)

// Checks runs checkers (plan 5). The zero value plus Home works.
type Checks struct {
	Graph *Graph // nil: DefaultGraph()
	Home  string // $SC_HOME: scratch copies go under Home/tmp
	Run   Runner

	exists       func(path string) bool            // nil: the path exists here; tests fake the machine
	lstat        func(string) (os.FileInfo, error) // nil: os.Lstat; tests fake the machine
	sshdHostKey  string                            // tests: a throwaway host key for sshd -t when not root
	netplanRoot  string                            // "": /; tests fake the machine's {lib,etc,run}/netplan
	nsswitchPath string                            // "": /etc/nsswitch.conf; tests fake the machine
	groupPath    string                            // "": /etc/group, whose sudo and admin members are the admins
	passwdPath   string                            // "": /etc/passwd, the users a group's members must be
	addrs        func() ([]netip.Addr, error)      // nil: this machine's interface addresses; tests fake the machine
	sshdRoot     string                            // "": /; tests fake the machine's /etc/ssh
}

// Report is the result of checking one file.
type Report struct {
	Checker  string    // "" when no checker reads the file
	Findings []Finding // sorted by line
	Notes    []string  // what could not be checked, e.g. "no validator found (findmnt)"
	// Said is what a validator printed behind a note, in its own words:
	// sc check shows it with -v, sc edit under the notes (M3 follow-up 4).
	// Like a finding's Raw, it may quote a file, so it never goes to the
	// journal.
	Said []string
	// Incomplete: a validator was cut short this time (killed, out of
	// time, output cut, or it exited without a word sc understands), so
	// findings may be missing. A problem it found before is then not known
	// to be gone.
	Incomplete bool
	// Unchecked: nothing was checked, and a note says why (a drop-in for
	// every unit with a prefix). sc check counts the file as not checked
	// (review of chunk F, B6).
	Unchecked bool
}

// input is what a checker gets.
type input struct {
	path string // the real path
	data []byte // the content to check
	file string // a scratch copy of data, named like path
	// saved is set for a version from the store (sc check <id>): what is
	// on disk at path now, its mode for one, is not that version's.
	saved bool
	// incomplete is set by validate when a validator did not run to the
	// end (Report.Incomplete).
	incomplete *bool
	// said collects Report.Said.
	said *[]string
	// unchecked is Report.Unchecked.
	unchecked *bool
}

// cut marks the report Incomplete and returns note.
func (in input) cut(note string) string {
	if in.incomplete != nil {
		*in.incomplete = true
	}
	return note
}

// skip marks the report Unchecked and returns note.
func (in input) skip(note string) string {
	if in.unchecked != nil {
		*in.unchecked = true
	}
	return note
}

// say keeps lines a validator printed for Report.Said and returns note,
// which is sc's own words only.
func (in input) say(note string, lines ...string) string {
	if in.said != nil {
		for _, l := range lines {
			if l != "" {
				*in.said = append(*in.said, l)
			}
		}
	}
	return note
}

// checkers maps the graph's checker names to their code.
var checkers = map[string]func(context.Context, *Checks, input) ([]Finding, []string, error){
	"fstab":      checkFstab,
	"sudoers":    checkSudoers,
	"sshd":       checkSshd,
	"unit":       checkUnit,
	"unitdropin": checkUnitDropIn,
	"shsyntax":   checkShSyntax,
	"grubcfg":    checkGrubCfg,
	"nsswitch":   checkNsswitch,
	"preload":    checkPreload,
	"flag":       checkFlag,
	"hosts":      checkHosts,
	"netplan":    checkNetplan,
	"udev":       checkUdev,
	"passwd":     checkPasswd,
	"group":      checkGroup,
	"sysctl":     checkSysctl,
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

// GraphInUse is the graph the checks use: Graph, or DefaultGraph().
func (c *Checks) GraphInUse() *Graph {
	if c.Graph != nil {
		return c.Graph
	}
	return DefaultGraph()
}

func (c *Checks) check(ctx context.Context, in input) (Report, error) {
	path, data := in.path, in.data
	g := c.GraphInUse()
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
	incomplete := false
	in.incomplete = &incomplete
	var said []string
	in.said = &said
	unchecked := false
	in.unchecked = &unchecked
	fs, notes, err := fn(ctx, c, in)
	if err != nil {
		return Report{}, fmt.Errorf("check %s: %w", path, err)
	}
	for i := range fs {
		fs[i].Path = path
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Line < fs[j].Line })
	return Report{Checker: name, Findings: fs, Notes: notes, Said: said, Incomplete: incomplete, Unchecked: unchecked}, nil
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
	return c.validateEnv(ctx, in, nil, tool, args...)
}

// validateEnv is validate with env added to the validator's environment.
func (c *Checks) validateEnv(ctx context.Context, in input, env []string, tool string, args ...string) (res Result, notes []string, ok bool, err error) {
	res, err = c.Run.RunEnv(ctx, filepath.Dir(in.file), env, tool, args...)
	// A validator that is not installed is missing before and after
	// alike: sc's own rules are then the whole check. One that was cut
	// short this time is not.
	if err == nil && res.Found && (res.TimedOut || res.Exit < 0 || res.Truncated) && in.incomplete != nil {
		*in.incomplete = true
	}
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

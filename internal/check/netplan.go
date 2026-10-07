//go:build linux

package check

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"smartconfig/internal/fsutil"
)

func init() {
	rules = append(rules,
		Rule{"netplan-invalid", Blocker, `netplan rejects the configuration. netplan apply and netplan try stop
with an error and change nothing, but at the next boot netplan leaves
out what it cannot read (a whole file, for a YAML error), so the
interfaces it configures may come up without an address: over SSH, the
way in. Fix the line sc check -v names; netplan try tests the fix.`},
	)
}

// netplanGenerate is netplan's generator, the program that runs at boot.
// sc runs it directly: "netplan generate" asks systemd to reload even
// with --root-dir (plan A7); the generator only writes under the root it
// is given. Started outside early boot it starts no unit: it does that
// only before basic.target (netplan-generate(8)), and sc runs after it.
const netplanGenerate = "/usr/libexec/netplan/generate"

// netplanDirs are where netplan reads *.yaml, lowest priority first: a
// file shadows one of the same name in an earlier directory, and all of
// them are merged in the lexical order of their names, wherever they are
// (netplan-generate(8)).
var netplanDirs = []string{"lib/netplan", "etc/netplan", "run/netplan"}

var (
	// The generator stops at the first error and prints one of:
	// "FILE:LINE:COL: MSG", then the YAML line it quotes and a caret;
	// "FILE: Error in network definition: ID: MSG" about a definition as a
	// whole; "ERROR: ..." from a writer, which names no file.
	netplanLineMsg = regexp.MustCompile(`^(/.*?):(\d+):(\d+): (.*)$`)
	netplanFileMsg = regexp.MustCompile(`^(/.*?): Error in network definition: (.*)$`)
	netplanDefID   = regexp.MustCompile(`^([^\s:'"]+): `)
)

// netplanFile is a file the generator reads, at the path netplan reads it
// from.
type netplanFile struct {
	path string
	data []byte
}

// checkNetplan checks a netplan file as netplan reads it: merged with all
// the machine's other netplan files, in a copy of their directories under
// the scratch directory, by netplan's generator. A saved version is
// merged with the other files as they are now, which is what restoring
// it would give. The generator stops at its first error: one in this file
// is a finding, one in another file a note, as this file was then not
// checked to the end.
func checkNetplan(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	dir, name := filepath.Dir(in.path), filepath.Base(in.path)
	if !slices.Contains(netplanDirs, strings.TrimPrefix(dir, "/")) || !strings.HasSuffix(name, ".yaml") || strings.HasPrefix(name, ".") {
		// netplan's glob skips a name with a leading dot.
		return nil, []string{in.skip("netplan does not read this file (it reads *.yaml, not names that start with a dot); it was not checked")}, nil
	}
	others, notes := c.netplanOthers(in.path)
	// Files sc could not read are left out of the merge: an error that
	// may come from one of them is no finding of this file.
	incomplete := slices.ContainsFunc(notes, func(n string) bool { return strings.HasPrefix(n, "could not ") })
	maybeOthers := func(msg string) bool {
		return incomplete && (strings.HasSuffix(msg, " is not defined") || strings.Contains(msg, " is already assigned to ") ||
			strings.HasSuffix(msg, " changes device type"))
	}
	partial := "netplan reports an error that may come from a file sc could not read; run sc check as root"
	scratch := filepath.Dir(in.file)
	root := filepath.Join(scratch, "root")
	if err := netplanTree(root, append([]netplanFile{{in.path, in.data}}, others...)); err != nil {
		return nil, notes, err
	}
	res, vnotes, ok, err := c.validate(ctx, in, netplanGenerate, "--root-dir", root)
	notes = append(notes, vnotes...)
	if err != nil || !ok || res.Exit == 0 {
		// With exit 0 what it says are warnings: a deprecated key, or a
		// file others may read (not sc's copies, which are 0600).
		return nil, notes, err
	}
	l := strings.ReplaceAll(netplanFirstError(res.Err), root, "")
	if l == "" {
		// None of its output is quoted: it may be a line of the file.
		return nil, append(notes, in.cut(fmt.Sprintf("netplan's generator failed (exit %d) without a message sc understands; the file was not checked", res.Exit))), nil
	}
	if m := netplanLineMsg.FindStringSubmatch(l); m != nil {
		n, _ := strconv.Atoi(m[2])
		if m[1] != in.path {
			return nil, append(notes, fmt.Sprintf("netplan stops at an error in %s, line %d: this file was not checked to the end", m[1], n)), nil
		}
		if maybeOthers(m[4]) {
			return nil, append(notes, partial), nil
		}
		return []Finding{{Rule: "netplan-invalid", Severity: Blocker, Line: n, Raw: l,
			Text: netplanText(m[4]), Key: lineKey(in.data, n) + " " + m[4]}}, notes, nil
	}
	if m := netplanFileMsg.FindStringSubmatch(l); m != nil {
		if m[1] != in.path {
			return nil, append(notes, fmt.Sprintf("netplan rejects a definition in %s: this file was not checked to the end", m[1])), nil
		}
		if incomplete {
			return nil, append(notes, partial), nil
		}
		return []Finding{{Rule: "netplan-invalid", Severity: Blocker, Line: netplanDefLine(in.data, m[2]), Raw: l,
			Text: "netplan rejects this device's definition as a whole", Key: m[2]}}, notes, nil
	}
	if strings.HasPrefix(l, "ERROR: cannot create ") {
		return nil, append(notes, "netplan's generator could not write its output in sc's scratch directory; the file was not checked to the end"), nil
	}
	if incomplete {
		return nil, append(notes, partial), nil
	}
	f := Finding{Rule: "netplan-invalid", Severity: Blocker, Raw: l, Key: l,
		Text: "netplan reads the file but cannot generate a network configuration from it"}
	if len(others) == 0 {
		return []Finding{f}, notes, nil
	}
	// A writer names no file: the error is this file's unless the other
	// files give it without this one.
	alone := filepath.Join(scratch, "others")
	if err := netplanTree(alone, others); err != nil {
		return nil, notes, err
	}
	res, _, ok, err = c.validate(ctx, in, netplanGenerate, "--root-dir", alone)
	if err != nil {
		return nil, notes, err
	}
	if ok && res.Exit != 0 && strings.ReplaceAll(netplanFirstError(res.Err), alone, "") == l {
		return nil, append(notes, "netplan cannot generate a network configuration from another of its files: this file was not checked to the end"), nil
	}
	return []Finding{f}, notes, nil
}

// netplanOthers reads the files netplan merges with path: every *.yaml of
// netplanDirs but path. A file of the same name is left out: shadowed by
// path in a directory before path's, and shadowing it in one after, which
// a note says (path is checked in its place). A file sc cannot read (as a
// user, 50-cloud-init.yaml is root's alone) is a note: the check is
// incomplete.
func (c *Checks) netplanOthers(path string) (others []netplanFile, notes []string) {
	base := c.netplanRoot
	if base == "" {
		base = "/"
	}
	rank := slices.Index(netplanDirs, strings.TrimPrefix(filepath.Dir(path), "/"))
	name := filepath.Base(path)
	for i, d := range netplanDirs {
		entries, err := os.ReadDir(filepath.Join(base, d))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				notes = append(notes, fmt.Sprintf("could not list /%s (%s), whose files netplan merges with this one: the check is incomplete", d, fsutil.ErrText(err)))
			}
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if !strings.HasSuffix(n, ".yaml") || strings.HasPrefix(n, ".") {
				continue
			}
			p := "/" + d + "/" + n
			if n == name {
				if i > rank {
					notes = append(notes, fmt.Sprintf("netplan reads %s instead of this file while it exists; this file was checked in its place", p))
				}
				continue
			}
			data, err := os.ReadFile(filepath.Join(base, d, n))
			if err != nil {
				notes = append(notes, fmt.Sprintf("could not read %s (%s), which netplan merges with this file: the check is incomplete", p, fsutil.ErrText(err)))
				continue
			}
			others = append(others, netplanFile{p, data})
		}
	}
	return others, notes
}

// netplanTree writes files under root, each at its own path below it, as
// the generator's --root-dir. The files are 0600: netplan warns about one
// that others may read.
func netplanTree(root string, files []netplanFile) error {
	for _, f := range files {
		p := filepath.Join(root, f.path)
		if err := scratchMkdir(filepath.Dir(p)); err != nil {
			return fmt.Errorf("netplan scratch root: %w", err)
		}
		if err := os.WriteFile(p, f.data, 0o600); err != nil {
			return fmt.Errorf("netplan scratch root: %w", err)
		}
	}
	return nil
}

// netplanFirstError returns the generator's error line, the first line
// in one of its shapes. Before it come GLib warnings (a deprecated key),
// after it the YAML line it quotes, which may hold a Wi-Fi password, and
// a caret: neither is kept.
func netplanFirstError(stderr []byte) string {
	for _, l := range strings.Split(string(stderr), "\n") {
		if netplanLineMsg.MatchString(l) || netplanFileMsg.MatchString(l) ||
			strings.HasPrefix(l, "ERROR: ") || strings.HasPrefix(l, "Error in network definition: ") {
			return l
		}
	}
	return ""
}

// netplanText turns the generator's message about a line into a sentence
// of ours. It never repeats a value: the file may hold Wi-Fi passwords.
func netplanText(msg string) string {
	def, isDef := strings.CutPrefix(msg, "Error in network definition: ")
	switch {
	case strings.HasPrefix(msg, "Invalid YAML: tabs"):
		return "the line is indented with a tab, which YAML does not allow"
	case strings.HasPrefix(msg, "Invalid YAML: inconsistent indentation"):
		return "the line's indentation does not fit the lines above it"
	case strings.HasPrefix(msg, "Invalid YAML: "):
		return "the file is not valid YAML at this line"
	case !isDef:
		return "netplan cannot read this line"
	case strings.HasPrefix(def, "unknown key "):
		return "netplan does not know the key on this line"
	case strings.HasSuffix(def, " is not defined"):
		return "the line names an interface that no netplan file defines"
	case strings.Contains(def, " is already assigned to "):
		return "the line names an interface that another bond or bridge already has"
	case strings.HasSuffix(def, " changes device type"):
		return "the line defines an interface again, as another type"
	case strings.HasPrefix(def, "expected "):
		return "the line has the wrong kind of value: a single value, a list or a mapping"
	case strings.HasPrefix(def, "invalid "), strings.HasPrefix(def, "malformed "), strings.HasPrefix(def, "unknown "):
		return "netplan does not accept the value on this line"
	}
	return "netplan does not accept this line"
}

// netplanDefLine returns the line of data that starts the definition a
// message names ("enp0s3: 'set-name:' requires 'match:' properties"), or
// 0.
func netplanDefLine(data []byte, msg string) int {
	m := netplanDefID.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	start := regexp.MustCompile(`^\s*["']?` + regexp.QuoteMeta(m[1]) + `["']?\s*:(\s|$)`)
	for i, l := range strings.Split(string(data), "\n") {
		if start.MatchString(l) {
			return i + 1
		}
	}
	return 0
}

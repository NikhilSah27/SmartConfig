//go:build linux

package check

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

func init() {
	rules = append(rules,
		Rule{"sudoers-syntax", Blocker, `visudo rejects a line. sudo 1.9.3 and later drops the rest of that line
and runs with what is left, so a rule that gave someone sudo may be gone;
older sudo refuses to run at all. Ubuntu locks root, so no sudo, no root.
A bad Defaults option or a missing include is only an error; an unused
or undefined alias is a warning. Fix the line; sc check -v shows where.`},
		Rule{"sudoers-mode", Error, `The file is not mode 0440 owned by root. sudo ignores a sudoers file
that root does not own or that others may write, so its rules do not
apply; for /etc/sudoers itself sudo does not run at all. Other modes
work, but visudo -c reports them (a warning).
Fix: chown root:root FILE and chmod 0440 FILE.`},
	)
}

var (
	// visudo's messages: "FILE:LINE:COL: what", "Warning: " in front of
	// the ones that are not errors.
	visudoMsg = regexp.MustCompile(`^(Warning: )?(/.*?):(\d+):(?:(\d+):)? (.*)$`)
	// After a syntax error visudo quotes the line, then marks the column.
	visudoCaret = regexp.MustCompile(`^ *\^~*$`)
	// An include line of a sudoers file. Relative to the file's directory
	// unless absolute; may be quoted.
	sudoersInclude = regexp.MustCompile(`^\s*[@#]include\s+"?([^"]+?)"?\s*$`)

	unknownDefault = regexp.MustCompile(`^unknown defaults entry "([^"]*)"$`)
	badDefault     = regexp.MustCompile(`^value ".*" is invalid for option "([^"]*)"$`)
	aliasTwice     = regexp.MustCompile(`^Alias "([^"]*)" already defined$`)
	aliasUndefined = regexp.MustCompile(`^(\w+) "([^"]*)" referenced but not defined$`)
	aliasUnused    = regexp.MustCompile(`^unused (\w+) "([^"]*)"$`)
)

// visudoText turns one of visudo's messages about line col into a
// sentence of ours that never repeats the file's line, and its severity
// from what sudo does with the file (sudoers(5)): a syntax error loses
// the line, a bad Defaults option is logged and ignored, an alias that
// is never used or never defined changes nothing. warned says visudo
// itself did not count the message as an error.
func visudoText(msg string, col int, warned bool) (string, Severity) {
	at := ""
	if col > 0 {
		at = fmt.Sprintf(" at column %d", col)
	}
	switch {
	case msg == "syntax error":
		return "syntax error" + at, Blocker
	case strings.HasPrefix(msg, "syntax error, "): // a reserved word used as an alias name
		return "syntax error" + at + ": a reserved word is used as an alias name", Blocker
	case msg == "unexpected line break in string":
		return "a quoted string is not closed" + at, Blocker
	case unknownDefault.MatchString(msg):
		return fmt.Sprintf("unknown Defaults option %q", unknownDefault.FindStringSubmatch(msg)[1]), Error
	case badDefault.MatchString(msg):
		return fmt.Sprintf("invalid value for Defaults option %q", badDefault.FindStringSubmatch(msg)[1]), Error
	case aliasTwice.MatchString(msg):
		return fmt.Sprintf("alias %q is defined twice", aliasTwice.FindStringSubmatch(msg)[1]), Blocker
	case aliasUndefined.MatchString(msg):
		m := aliasUndefined.FindStringSubmatch(msg)
		return fmt.Sprintf("%s %q is used but never defined", m[1], m[2]), Warning
	case aliasUnused.MatchString(msg):
		m := aliasUnused.FindStringSubmatch(msg)
		return fmt.Sprintf("%s %q is defined but never used", m[1], m[2]), Warning
	case warned:
		return "visudo warns about the line" + at, Warning
	}
	return "visudo rejects the line" + at, Blocker
}

// sudoersMode checks the file as it is on disk against what sudo expects
// of a sudoers file, mode 0440 owned by root (sudoers(5)). sudo ignores a
// file root does not own, one others may write, and one its group may
// write unless that group is root's: an error. Any other mode works, and
// only visudo -c complains: a warning. found is false
// when there is nothing to say: the file is fine, or does not exist yet
// (sc edit creating a drop-in), or is not a regular file (a symlink's
// mode is its target's). note says when the mode could not be read.
func (c *Checks) sudoersMode(path string) (f Finding, found bool, note string) {
	lstat := c.lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	fi, err := lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Finding{}, false, ""
	case err != nil:
		return Finding{}, false, fmt.Sprintf("could not read the mode of %s (%v); sudoers-mode was not checked", path, err)
	case !fi.Mode().IsRegular():
		return Finding{}, false, ""
	}
	mode, uid, gid := uint32(fi.Mode().Perm()), -1, -1
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		mode, uid, gid = st.Mode&0o7777, int(st.Uid), int(st.Gid)
	}
	if mode == 0o440 && uid <= 0 {
		return Finding{}, false, ""
	}
	f = Finding{Rule: "sudoers-mode", Severity: Warning, Text: fmt.Sprintf("the file is mode %04o, not 0440", mode)}
	if uid >= 0 {
		f.Text = fmt.Sprintf("the file is mode %04o and owned by uid %d, not 0440 root", mode, uid)
	}
	if uid > 0 || mode&0o002 != 0 || (mode&0o020 != 0 && gid != 0) {
		f.Severity = Error // sudo ignores the file
	}
	return f, true, ""
}

// checkSudoers checks /etc/sudoers or one of its drop-ins: the mode and
// owner of the file on disk, then visudo -c on the content. visudo
// follows the file's includes, so what it says about other files is a
// note, not a finding of this file: a change to a drop-in checks that
// file alone (plan 7).
func checkSudoers(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	var out []Finding
	var notes []string
	if f, found, note := c.sudoersMode(in.path); found {
		out = append(out, f)
	} else if note != "" {
		notes = append(notes, note)
	}
	res, vnotes, ok, err := c.validate(ctx, in, "visudo", "-c", "-f", in.file)
	notes = append(notes, vnotes...)
	if err != nil || !ok {
		return out, notes, err
	}
	lines := strings.Split(string(in.data), "\n")
	// key tells apart two lines with the same problem at the same column,
	// and follows a line that only moved.
	key := func(n int) string {
		if n < 1 || n > len(lines) {
			return ""
		}
		sum := sha256.Sum256([]byte(lines[n-1]))
		return hex.EncodeToString(sum[:8])
	}
	// visudo read a scratch copy: its messages name that, and resolve a
	// relative include against its directory.
	scratchDir, realDir := filepath.Dir(in.file)+"/", filepath.Dir(in.path)+"/"
	real := func(s string) string { return strings.ReplaceAll(s, scratchDir, realDir) }
	type include struct {
		line     int
		relative bool
	}
	includes := map[string]include{} // the path visudo would print -> its line
	for i, l := range lines {
		if m := sudoersInclude.FindStringSubmatch(l); m != nil {
			p, rel := m[1], !strings.HasPrefix(m[1], "/")
			if rel {
				p = realDir + p
			}
			if _, dup := includes[p]; !dup {
				includes[p] = include{i + 1, rel}
			}
		}
	}
	said := false // visudo said something sc understood
	errLines := strings.Split(strings.TrimRight(string(res.Err), "\n"), "\n")
	for i := 0; i < len(errLines); i++ {
		l := errLines[i]
		if m := visudoMsg.FindStringSubmatch(l); m != nil {
			raw := real(l)
			if i+2 < len(errLines) && visudoCaret.MatchString(errLines[i+2]) {
				raw += "\n" + errLines[i+1] + "\n" + errLines[i+2]
				i += 2
			}
			n, _ := strconv.Atoi(m[3])
			col, _ := strconv.Atoi(m[4])
			said = true
			if m[2] != in.file {
				p := real(m[2])
				notes = append(notes, fmt.Sprintf("visudo reports a problem in %s, line %d, which this file includes; sc check %s shows it", p, n, p))
				continue
			}
			text, sev := visudoText(m[5], col, m[1] != "" || res.Exit == 0)
			out = append(out, Finding{Rule: "sudoers-syntax", Severity: sev, Line: n, Text: text, Raw: raw, Key: key(n)})
			continue
		}
		// "visudo: PATH: No such file or directory" and the like: what it
		// could not read. An include of this file that names a file which
		// does not exist is this file's problem, when sc can see that it
		// is not the scratch copy's directory that is missing it.
		if msg, isOwn := strings.CutPrefix(l, "visudo: "); isOwn {
			msg = real(msg)
			said = true
			if p, reason, found := strings.Cut(msg, ": "); found && reason == "No such file or directory" {
				if inc, ok := includes[p]; ok {
					if inc.relative && c.pathExists(p) {
						notes = append(notes, fmt.Sprintf("%s, included at line %d, was not checked: visudo looks for a relative include next to the copy it reads", p, inc.line))
						continue
					}
					out = append(out, Finding{Rule: "sudoers-syntax", Severity: Error, Line: inc.line, Raw: "visudo: " + msg, Key: key(inc.line),
						Text: "includes a file that does not exist"})
					continue
				}
			}
			notes = append(notes, "visudo could not read everything ("+msg+"); the included files were not all checked")
		}
	}
	if res.Exit != 0 && !said {
		notes = append(notes, fmt.Sprintf("visudo exited %d without a message sc understands; the file was not checked", res.Exit))
	}
	return out, notes, nil
}

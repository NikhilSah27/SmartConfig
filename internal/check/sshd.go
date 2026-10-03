//go:build linux

package check

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func init() {
	rules = append(rules,
		Rule{"sshd-invalid", Blocker, `sshd refuses this configuration. The running server keeps going, but
the next start of ssh.service checks the file, fails and is not retried,
so after a restart or a reboot there is no SSH access.
Fix the line sshd names (sc check -v shows its message); sshd -t checks
the file again.`},
	)
}

var (
	// sshd's two shapes for a message about a line: "F: line N: Bad
	// configuration option: KW" (counted, then "F: terminating, N bad
	// configuration options") and "F line N: MSG" (fatal). F is at the
	// start, after the scratch path is replaced by the real one.
	sshdLine = regexp.MustCompile(`^(.*?):? line ([0-9]+): (.*)$`)
	// Not errors: sshd says them and goes on.
	sshdNotice = regexp.MustCompile(`^(Deprecated|Unsupported) option \S+$`)
	// About the machine, not the file. The first three come after the
	// file is parsed (as root, before -t says it is fine); the last two
	// are its host keys, which only root can read.
	sshdEnv = regexp.MustCompile(`^(Missing privilege separation directory|Privilege separation user |.* must be owned by root and not group or world-writable\.$|sshd: no hostkeys available|Unable to load host key)`)
)

// checkSshd checks an sshd_config or a drop-in with sshd -t. The server
// reads its host keys before it says the file is fine, which only root
// can; a test gives it a throwaway key with -h instead. A drop-in is
// checked on its own: it may use the same keywords, and a setting that
// clashes with the main file is sshd's to report.
func checkSshd(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	args := []string{"-t", "-f", in.file}
	if c.sshdHostKey != "" {
		args = append(args, "-h", c.sshdHostKey)
	}
	res, notes, ok, err := c.validate(ctx, in, "sshd", args...)
	if err != nil || !ok {
		return nil, notes, err
	}
	// Its messages all go to stderr. With exit 0 they are notices
	// (deprecated options) and the file is fine.
	if res.Exit == 0 {
		return nil, notes, nil
	}
	said := len(notes)
	var out []Finding
	byLine := map[int]int{} // line -> index in out
	var loose []string      // lines that name no file, kept for the next finding
	var env []string        // lines about the machine
	for _, l := range strings.Split(strings.ReplaceAll(string(res.Err), in.file, in.path), "\n") {
		l = strings.TrimRight(l, "\r") // its log lines end in \r\n
		if l == "" || strings.HasSuffix(l, " bad configuration options") {
			continue
		}
		if sshdEnv.MatchString(l) {
			env = append(env, l)
			continue
		}
		m := sshdLine.FindStringSubmatch(l)
		if m == nil {
			if msg, isOwn := strings.CutPrefix(l, in.path+": "); isOwn {
				// "F: No such file or directory": it never read the file.
				return nil, append(notes, "sshd could not check the file ("+msg+")"), nil
			}
			loose = append(loose, l)
			continue
		}
		file, n, msg := m[1], m[2], m[3]
		if file != in.path {
			notes = append(notes, "sshd reports a problem in another file: "+l)
			loose = nil
			continue
		}
		if sshdNotice.MatchString(msg) {
			continue
		}
		line, _ := strconv.Atoi(n)
		raw := strings.Join(append(loose, l), "; ")
		loose = nil
		if i, seen := byLine[line]; seen {
			// An unknown keyword in a Match block gets two messages.
			out[i].Raw += "; " + raw
			continue
		}
		f := Finding{Rule: "sshd-invalid", Severity: Blocker, Line: line, Raw: raw, Key: msg}
		if kw, isUnknown := strings.CutPrefix(msg, "Bad configuration option: "); isUnknown {
			f.Text = fmt.Sprintf("unknown option %q", kw)
		} else if kw := sshdKeyword(in.data, line); kw != "" {
			f.Text = "sshd does not accept this " + kw + " line"
		} else {
			f.Text = "sshd does not accept this line"
		}
		byLine[line] = len(out)
		out = append(out, f)
	}
	if len(loose) > 0 {
		// sshd refuses the configuration without naming a line (a
		// ListenAddress that does not resolve, AuthorizedKeysCommand
		// without its user). Only 255 is its fatal exit: anything else
		// means it stopped before it had read the file.
		raw := strings.Join(loose, "; ")
		if res.Exit == 255 {
			out = append(out, Finding{Rule: "sshd-invalid", Severity: Blocker, Raw: raw, Key: raw,
				Text: "sshd refuses the configuration, without naming a line"})
		} else {
			notes = append(notes, "sshd could not check the file ("+raw+")")
		}
	}
	if len(env) > 0 {
		out, notes = sshdHostKeys(c, in, env, out, notes)
	}
	if len(out) == 0 && len(notes) == said {
		notes = append(notes, fmt.Sprintf("sshd failed (exit %d) without naming a problem; the file was not checked", res.Exit))
	}
	return out, notes, nil
}

// sshdHostKeys reads the lines about the machine. "no hostkeys available"
// after keys it could not load, named by HostKey lines of this file, none
// of which exists, is the file's fault: the server would not start. A key
// that exists is one a user cannot read (as root sshd loads it), and no
// key named at all means the default ones, which sshd does not name: a
// note either way, since the check is not finished.
func sshdHostKeys(c *Checks, in input, env []string, out []Finding, notes []string) ([]Finding, []string) {
	var keyless []string
	none := false
	for _, l := range env {
		if p, isKey := strings.CutPrefix(l, "Unable to load host key: "); isKey {
			keyless = append(keyless, p)
		}
		none = none || strings.HasPrefix(l, "sshd: no hostkeys available")
	}
	missing := none && len(keyless) > 0
	for _, p := range keyless {
		if c.pathExists(p) {
			missing = false
		}
	}
	switch {
	case missing:
		raw := strings.Join(env, "; ")
		for _, p := range keyless {
			if line := sshdHostKeyLine(in.data, p); line > 0 {
				out = append(out, Finding{Rule: "sshd-invalid", Severity: Blocker, Line: line, Raw: raw, Key: p,
					Text: "host key " + p + " does not exist, and sshd has no other"})
			} else {
				notes = append(notes, "sshd reports a problem in another file: host key "+p+" does not exist")
			}
		}
	case none:
		notes = append(notes, "sshd found no host key it may read (the machine's are root's); the file was not checked to the end")
	default:
		notes = append(notes, "sshd did not finish checking the file ("+strings.Join(env, "; ")+")")
	}
	return out, notes
}

// sshdKeyword returns the keyword on line n of an sshd_config (its first
// word, before a space or "="), when it looks like one, else "".
func sshdKeyword(data []byte, n int) string {
	lines := strings.Split(string(data), "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	kw := strings.FieldsFunc(lines[n-1], func(r rune) bool { return r == ' ' || r == '\t' || r == '=' })
	if len(kw) == 0 || strings.Trim(kw[0], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" {
		return ""
	}
	return kw[0]
}

// sshdHostKeyLine returns the first line whose HostKey names path, or 0.
func sshdHostKeyLine(data []byte, path string) int {
	for i, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(strings.ReplaceAll(l, "=", " "))
		if len(f) > 1 && strings.EqualFold(f[0], "HostKey") && strings.Trim(f[1], `"`) == path {
			return i + 1
		}
	}
	return 0
}

//go:build linux

package check

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
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
		Rule{"sshd-invalid", Blocker, `sshd refuses this configuration. The running server keeps going, but
the next start of ssh.service checks the file, fails and is not retried,
so after a restart or a reboot there is no SSH access.
Fix the line sshd names (sc check -v shows its message); sshd -t checks
the file again.`},
		Rule{"sshd-listen-missing", Warning, `ListenAddress names an address no interface of this machine has now.
sshd takes no connections there until it appears (ssh.socket binds it
and waits; an sshd binding for itself skips it), and when it is the only
address, SSH is out of reach after the next restart or boot. Use one the
machine has (ip -brief address), or none to listen on all of them.`},
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

// sshdMain is sshd's own file; relative Include paths are in its directory.
const sshdMain = "/etc/ssh/sshd_config"

// checkSshd checks an sshd_config or a drop-in with sshd -t, a drop-in as
// sshd reads it, inside sshd_config; and with sc's own rule for
// ListenAddress, which sshd -t does not judge.
func checkSshd(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	var out []Finding
	var notes []string
	var err error
	if in.path == sshdMain {
		var others []string
		out, notes, others, err = sshdRun(ctx, c, in, in.file, strings.NewReplacer(in.file, in.path), in.say)
		notes = append(notes, sshdOthers(in, others)...)
	} else {
		out, notes, err = sshdTogether(ctx, c, in)
	}
	if err != nil {
		return nil, nil, err
	}
	return append(out, sshdListen(c, in)...), notes, nil
}

// sshdListen finds the ListenAddress lines whose address no interface of
// this machine has (M3 follow-up 2): sshd -t passes them, as it binds
// nothing. A host name is not looked up, and the wildcard and loopback
// addresses are always there. On a machine with no address but loopback
// (a rescue boot, which starts no network) nothing is known to be
// missing.
func sshdListen(c *Checks, in input) []Finding {
	have, err := c.machineAddrs()
	if err != nil || !slices.ContainsFunc(have, func(a netip.Addr) bool { return !a.IsLoopback() }) {
		return nil
	}
	var out []Finding
	for i, l := range strings.Split(string(in.data), "\n") {
		f := strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == '\t' || r == '=' })
		if len(f) < 2 || !strings.EqualFold(f[0], "ListenAddress") {
			continue
		}
		a, ok := sshdListenAddr(f[1])
		if !ok || a.IsUnspecified() || a.IsLoopback() || slices.Contains(have, a) {
			continue
		}
		out = append(out, Finding{Rule: "sshd-listen-missing", Severity: Warning, Line: i + 1, Key: lineKey(in.data, i+1),
			Text: fmt.Sprintf("no interface of this machine has %s now; sshd takes no connections there", a)})
	}
	return out
}

// sshdListenAddr returns the address a ListenAddress value names, ok false
// for a host name: 192.0.2.1, 192.0.2.1:22, 2001:db8::1, [2001:db8::1]:22,
// fe80::1%eth0, each maybe in quotes.
func sshdListenAddr(v string) (netip.Addr, bool) {
	v = strings.Trim(v, `"`)
	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	} else {
		v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	}
	v, _, _ = strings.Cut(v, "%")
	a, err := netip.ParseAddr(v)
	return a.Unmap(), err == nil
}

// machineAddrs returns the addresses of this machine's interfaces.
func (c *Checks) machineAddrs() ([]netip.Addr, error) {
	if c.addrs != nil {
		return c.addrs()
	}
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, x := range as {
		if n, ok := x.(*net.IPNet); ok {
			if a, ok := netip.AddrFromSlice(n.IP); ok {
				out = append(out, a.Unmap())
			}
		}
	}
	return out, nil
}

// sshdOthers turns what sshd said about other files than the one checked
// into notes, one per file, its words kept for -v.
func sshdOthers(in input, others []string) []string {
	var notes []string
	for _, raw := range others {
		note := "sshd reports a problem in another file"
		if m := sshdLine.FindStringSubmatch(raw); m != nil {
			note += ", " + m[1]
		}
		if note = in.say(note, raw); !slices.Contains(notes, note) {
			notes = append(notes, note)
		}
	}
	return notes
}

// sshdTogether checks a drop-in as sshd reads it (M3 follow-up 3): in a
// scratch copy of sshd_config whose Include of the drop-in's directory
// names a scratch copy of that directory, the candidate in place of its
// namesake. A setting whose partner is in sshd_config (AuthorizedKeysCommand
// and its user) is then no false blocker. What sshd says about the
// drop-in's lines is the drop-in's; what it says about another file, or
// without a line, only when a second run with the candidate empty does not
// say it too. The drop-in is checked alone, with a note, when sshd_config
// does not include it, when it or another drop-in cannot be read, or when
// sshd stops at a problem of another file's, which may come before the
// drop-in is read. Without an sshd_config there is nothing to read it in.
func sshdTogether(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	alone := func(why string) ([]Finding, []string, error) {
		out, notes, others, err := sshdRun(ctx, c, in, in.file, strings.NewReplacer(in.file, in.path), in.say)
		notes = append(notes, sshdOthers(in, others)...)
		if why != "" {
			notes = append(notes, why+"; the drop-in was checked alone")
		}
		return out, notes, err
	}
	main, err := os.ReadFile(filepath.Join(c.sshdRoot, sshdMain))
	if errors.Is(err, fs.ErrNotExist) {
		return alone("")
	} else if err != nil {
		return alone("sc could not read " + sshdMain + " (" + fsutil.ErrText(err) + ")")
	}
	// Each Include pattern that names the drop-in is pointed at the copy
	// of its directory.
	dir, name := filepath.Dir(in.path), filepath.Base(in.path)
	root := filepath.Join(filepath.Dir(in.file), "together")
	copies := filepath.Join(root, "d")
	var pats []string
	lines := strings.Split(string(main), "\n")
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.EqualFold(f[0], "Include") {
			continue
		}
		named := false
		for j, p := range f[1:] {
			p = strings.Trim(p, `"`)
			if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(sshdMain), p)
			}
			if ok, _ := filepath.Match(p, in.path); ok && filepath.Dir(p) == dir {
				pats = append(pats, filepath.Base(p))
				f[j+1], named = filepath.Join(copies, filepath.Base(p)), true
			}
		}
		if named {
			lines[i] = strings.Join(f, " ")
		}
	}
	if len(pats) == 0 {
		return alone(sshdMain + " does not include it")
	}
	if err := os.MkdirAll(copies, 0o700); err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(filepath.Join(c.sshdRoot, dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return alone("sc could not read " + dir + " (" + fsutil.ErrText(err) + ")")
	}
	for _, e := range entries {
		read := false
		for _, p := range pats {
			ok, _ := filepath.Match(p, e.Name())
			read = read || ok
		}
		if !read || e.Name() == name {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.sshdRoot, dir, e.Name()))
		if err != nil {
			return alone("sc could not read " + filepath.Join(dir, e.Name()) + " (" + fsutil.ErrText(err) + ")")
		}
		if err := os.WriteFile(filepath.Join(copies, e.Name()), b, 0o600); err != nil {
			return nil, nil, err
		}
	}
	file := filepath.Join(root, "sshd_config")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return nil, nil, err
	}
	names := strings.NewReplacer(file, sshdMain, copies+"/", dir+"/")
	// What sshd says in the run whose result is kept goes to Report.Said;
	// the second run's, and a run given up for the drop-in alone, does not.
	var heard []string
	hear := func(note string, lines ...string) string { heard = append(heard, lines...); return note }
	quiet := func(note string, _ ...string) string { return note }
	run := func(data []byte, say func(string, ...string) string) ([]Finding, []string, []string, error) {
		if err := os.WriteFile(filepath.Join(copies, name), data, 0o600); err != nil {
			return nil, nil, nil, err
		}
		return sshdRun(ctx, c, in, file, names, say)
	}
	out, notes, others, err := run(in.data, hear)
	if err != nil {
		return nil, nil, err
	}
	lineless := func(f Finding) bool { return f.Line == 0 }
	if !slices.ContainsFunc(out, lineless) && len(others) == 0 {
		return out, notes, nil
	}
	before, _, othersBefore, err := run(nil, quiet)
	if err != nil {
		return nil, nil, err
	}
	if len(othersBefore) > 0 {
		return alone("sshd stops at a problem in another file, maybe before the drop-in")
	}
	in.say("", heard...)
	// A refusal without a line that sshd gives without the drop-in too is
	// not the drop-in's doing; sshd stopped there, so the drop-in was not
	// checked to the end.
	var kept []string
	had := map[string]bool{}
	for _, f := range before {
		if lineless(f) {
			had[f.Key] = true
		}
	}
	out = slices.DeleteFunc(out, func(f Finding) bool {
		if lineless(f) && had[f.Key] {
			kept = append(kept, in.say("sshd refuses the configuration without the drop-in too; the drop-in was not checked to the end", f.Raw))
		}
		return lineless(f) && had[f.Key]
	})
	// Without the drop-in sshd reads every other file content, so what it
	// refuses in one now is the drop-in's doing (a block the drop-in leaves
	// open would do that): a finding of the drop-in's, without a line.
	for _, raw := range others {
		out = append(out, Finding{Rule: "sshd-invalid", Severity: Blocker, Raw: raw, Key: raw,
			Text: "with this drop-in, sshd refuses another file's line"})
	}
	return out, append(kept, notes...), nil
}

// sshdRun runs sshd -t on file, a scratch copy, and reads what it says,
// with names putting the real paths back; others are its lines about other
// files, and say keeps its words behind a note. The server reads its host
// keys before it says the file is fine, which only root can; a test gives
// it a throwaway key with -h instead.
func sshdRun(ctx context.Context, c *Checks, in input, file string, names *strings.Replacer, say func(string, ...string) string) (out []Finding, notes, others []string, err error) {
	args := []string{"-t", "-f", file}
	if c.sshdHostKey != "" {
		args = append(args, "-h", c.sshdHostKey)
	}
	res, notes, ok, err := c.validate(ctx, in, "sshd", args...)
	if err != nil || !ok {
		return nil, notes, nil, err
	}
	// Its messages all go to stderr. With exit 0 they are notices
	// (deprecated options) and the file is fine.
	if res.Exit == 0 {
		return nil, notes, nil, nil
	}
	said := len(notes)
	byLine := map[int]int{} // line -> index in out
	var loose []string      // lines that name no file, kept for the next finding
	var env []string        // lines about the machine
	for _, l := range strings.Split(names.Replace(string(res.Err)), "\n") {
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
			if strings.HasPrefix(l, in.path+": ") {
				// "F: No such file or directory": it never read the file.
				return nil, append(notes, say("sshd could not check the file", l)), nil, nil
			}
			loose = append(loose, l)
			continue
		}
		file, n, msg := m[1], m[2], m[3]
		if file != in.path {
			others = append(others, l)
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
		// The line's content in the key: an edit that adds the same
		// mistake above an old one is blamed for the new line.
		f := Finding{Rule: "sshd-invalid", Severity: Blocker, Line: line, Raw: raw, Key: lineKey(in.data, line) + " " + msg}
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
			notes = append(notes, say("sshd could not check the file", raw))
		}
	}
	if len(env) > 0 {
		out, notes = sshdHostKeys(c, in, env, out, notes, say)
	}
	if len(out) == 0 && len(notes) == said && len(others) == 0 {
		notes = append(notes, fmt.Sprintf("sshd failed (exit %d) without naming a problem; the file was not checked", res.Exit))
	}
	return out, notes, others, nil
}

// sshdHostKeys reads the lines about the machine. "no hostkeys available"
// after keys it could not load, named by HostKey lines of this file, none
// of which exists, is the file's fault: the server would not start. A key
// that exists is one a user cannot read (as root sshd loads it), and no
// key named at all means the default ones, which sshd does not name: a
// note either way, since the check is not finished.
func sshdHostKeys(c *Checks, in input, env []string, out []Finding, notes []string, say func(string, ...string) string) ([]Finding, []string) {
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
		notes = append(notes, say("sshd did not finish checking the file", env...))
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

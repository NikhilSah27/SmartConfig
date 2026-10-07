//go:build linux

package check

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

func init() {
	rules = append(rules,
		Rule{"sysctl-invalid", Error, `The line is not "key = value", or its key is empty or leads out of
/proc/sys. At boot systemd-sysctl skips the line, applies the rest and
then exits with an error, so systemd-sysctl.service fails at every boot.
"#" or ";" starts a comment line; "-key" alone keeps a key out of a glob.`},
		Rule{"sysctl-unknown-key", Warning, `This kernel has no such setting now, or it is read-only. At boot
systemd-sysctl skips the line and says so only at debug level, so the
setting silently does not apply: most likely a typo. A setting of a
module that is not loaded yet (net.bridge, nf_conntrack) appears once it
is; write "-key = value" when that is expected.`},
	)
}

var (
	// procps sysctl's messages on stderr. Only a line without "=" gets a
	// line number; the others name the key or its path.
	sysctlSyntax   = regexp.MustCompile(`^sysctl: .*\((\d+)\): invalid syntax, continuing\.\.\.$`)
	sysctlNoKey    = regexp.MustCompile(`^sysctl: cannot stat /proc/sys/(.*): No such file or directory$`)
	sysctlReadOnly = regexp.MustCompile(`^sysctl: setting key "(.*)"(?:: .*)?$`)
	sysctlOutside  = regexp.MustCompile(`^sysctl: Path is not under /proc/sys/: (.*)$`)
	// A per-interface setting (sysctl.d(5)): udev's 99-systemd.rules runs
	// systemd-sysctl again for these when the interface appears.
	sysctlIface = regexp.MustCompile(`^(net/ipv[46]/(?:conf|neigh))/([^/]+)/(.+)$`)
)

// sysctlLine is an assignment of a sysctl.d file as systemd-sysctl reads
// it (sysctl.d(5)): blanks around the line, the key and the value are
// dropped, "#" and ";" start a comment line, and a "-" before the key
// means a failure to set it is ignored.
type sysctlLine struct {
	n    int
	key  string // as written, without the "-"
	path string // under /proc/sys
	dash bool
	used bool // a message of sysctl's was about this line
}

func sysctlLines(data []byte) []*sysctlLine {
	var out []*sysctlLine
	for i, l := range strings.Split(string(data), "\n") {
		l = strings.TrimFunc(l, isSpaceC)
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		k, _, ok := strings.Cut(l, "=")
		if !ok {
			continue // not an assignment: sysctl says so with the line number
		}
		dash := strings.HasPrefix(k, "-")
		k = strings.TrimFunc(strings.TrimPrefix(k, "-"), isSpaceC)
		out = append(out, &sysctlLine{n: i + 1, key: k, path: sysctlPath(k), dash: dash})
	}
	return out
}

// sysctlPath turns a key into its path under /proc/sys (sysctl.d(5)):
// when its first separator is a dot, dots and slashes swap.
func sysctlPath(key string) string {
	i := strings.IndexAny(key, "./")
	if i < 0 || key[i] == '/' {
		return key
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '.':
			return '/'
		case '/':
			return '.'
		}
		return r
	}, key)
}

// take returns the first line no message was about yet that match
// accepts, or nil. sysctl reads the file in order and says one thing per
// line, so equal keys are matched in turn.
func take(lines []*sysctlLine, match func(*sysctlLine) bool) *sysctlLine {
	for _, l := range lines {
		if !l.used && match(l) {
			l.used = true
			return l
		}
	}
	return nil
}

// checkSysctl checks /etc/sysctl.conf or a file in /etc/sysctl.d with
// procps' sysctl --dry-run -p, which reads the file and looks up each key
// under /proc/sys but writes nothing (checked with strace and by reading
// the values before and after). sysctl reports a key with a "-" before it
// like any other, and systemd-sysctl ignores its failure: no finding. A
// setting of an interface that is not here now is set when it appears,
// if the kernel has that setting for the default interface.
func checkSysctl(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	res, notes, ok, err := c.validate(ctx, in, "sysctl", "--dry-run", "-p", in.file)
	if err != nil || !ok {
		return nil, notes, err
	}
	lines := sysctlLines(in.data)
	key := func(l *sysctlLine) (int, string) {
		if l == nil {
			return 0, ""
		}
		return l.n, lineKey(in.data, l.n)
	}
	var out []Finding
	var unknown []string
	said := false // sysctl said something sc understands
	for _, raw := range strings.Split(string(res.Err), "\n") {
		if raw == "" {
			continue
		}
		raw = strings.ReplaceAll(raw, in.file, in.path)
		said = true
		switch {
		case sysctlSyntax.MatchString(raw):
			n, _ := strconv.Atoi(sysctlSyntax.FindStringSubmatch(raw)[1])
			out = append(out, Finding{Rule: "sysctl-invalid", Severity: Error, Line: n, Key: lineKey(in.data, n), Raw: raw,
				Text: "the line is not an assignment (key = value): systemd-sysctl skips it and fails at boot"})
		case sysctlNoKey.MatchString(raw):
			p := sysctlNoKey.FindStringSubmatch(raw)[1]
			l := take(lines, func(l *sysctlLine) bool { return l.path == p })
			if l != nil && (l.dash || c.ifaceLater(p)) {
				continue
			}
			name := strings.ReplaceAll(p, "/", ".")
			if l != nil {
				name = l.key
			}
			n, k := key(l)
			out = append(out, Finding{Rule: "sysctl-unknown-key", Severity: Warning, Line: n, Key: k, Raw: raw,
				Text: quoteOdd(name) + " is not a setting of this kernel (a typo, or a module that is not loaded): systemd-sysctl skips the line"})
		case sysctlReadOnly.MatchString(raw):
			name := sysctlReadOnly.FindStringSubmatch(raw)[1]
			l := take(lines, func(l *sysctlLine) bool { return strings.ReplaceAll(l.path, "/", ".") == name })
			if l != nil && l.dash {
				continue
			}
			n, k := key(l)
			out = append(out, Finding{Rule: "sysctl-unknown-key", Severity: Warning, Line: n, Key: k, Raw: raw,
				Text: quoteOdd(name) + " cannot be set (it is read-only): systemd-sysctl skips the line"})
		case sysctlOutside.MatchString(raw):
			l := take(lines, func(l *sysctlLine) bool {
				p := path.Clean("/proc/sys/" + l.path)
				return !strings.HasPrefix(p, "/proc/sys/")
			})
			if l != nil && l.dash {
				continue
			}
			n, k := key(l)
			out = append(out, Finding{Rule: "sysctl-invalid", Severity: Error, Line: n, Key: k, Raw: raw,
				Text: "the key is empty or leads out of /proc/sys: systemd-sysctl fails at boot"})
		case strings.HasPrefix(raw, "sysctl: separators should not be repeated: "):
			// systemd-sysctl simplifies the path and sets the key.
		default:
			unknown = append(unknown, raw)
		}
	}
	if len(unknown) > 0 {
		notes = append(notes, in.say("sysctl printed something sc does not understand; the file may not have been checked", unknown[0]))
	} else if res.Exit != 0 && !said {
		notes = append(notes, fmt.Sprintf("sysctl exited %d without a message sc understands; the file was not checked", res.Exit))
	}
	return out, notes, nil
}

// ifaceLater reports whether p is a setting of a network interface that is
// not here now, of a kind the kernel has for its default interface:
// systemd-sysctl sets it when the interface appears.
func (c *Checks) ifaceLater(p string) bool {
	m := sysctlIface.FindStringSubmatch(p)
	return m != nil && m[2] != "default" && m[2] != "all" &&
		!c.pathExists("/proc/sys/"+m[1]+"/"+m[2]) && c.pathExists("/proc/sys/"+m[1]+"/default/"+m[3])
}

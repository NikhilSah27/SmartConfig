//go:build linux

package check

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// The plain rules (plan 5.4): files that have no validator program, only
// rules of ours. None of these checkers runs a tool.

func init() {
	rules = append(rules,
		Rule{"nsswitch-no-files", Blocker, `The line names neither files nor compat, so what /etc/passwd, group or
shadow holds cannot be looked up (systemd alone gives only root and
nobody). For passwd the users vanish: login, su, sudo and ssh fail; for
group, membership (sudo) is lost; for shadow, passwords cannot be
checked. Put files first: "passwd: files systemd" is what Ubuntu ships.`},
		Rule{"preload-missing-lib", Blocker, `/etc/ld.so.preload names a library that does not exist on this machine.
The loader reads this file for every dynamically linked program started
from now on, and each one prints "ERROR: ld.so: object ... cannot be
preloaded" before it runs; programs that read their own stderr fail.
Fix the path or remove the line. The static sc binary is not affected.`},
		Rule{"flag-nologin", Warning, `/etc/nologin exists. While it does, pam_nologin lets only root log in:
the console, ssh and the desktop refuse every other user and show the
file's text instead. Delete the file to allow logins again. (systemd
makes /run/nologin during shutdown; that one goes away by itself.)`},
		Rule{"flag-sshd-not-to-be-run", Warning, `/etc/ssh/sshd_not_to_be_run exists. ssh.service and ssh.socket carry
ConditionPathExists=!/etc/ssh/sshd_not_to_be_run, so the SSH server does
not start at boot or on systemctl start while the file is there; a
server already running keeps going until it stops.
Delete the file (Debian's way of switching sshd off), then start ssh.`},
		Rule{"hosts-no-localhost", Warning, `No line in /etc/hosts maps localhost to a loopback address (127.0.0.1
or ::1). Programs that connect to localhost then depend on DNS: with
systemd-resolved the name is still answered, without it they fail.
Add "127.0.0.1 localhost" and "::1 localhost ip6-localhost ip6-loopback".`},
	)
}

// isSpaceC is C's isspace: what glibc splits these files on.
func isSpaceC(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r'
}

// nsswitchLine is the last line for one database: glibc keeps the last.
type nsswitchLine struct {
	line     int
	services []string
}

// parseNsswitch reads an nsswitch.conf as glibc 2.33+ does
// (nss/nss_database.c, nss_action_parse.c), which is not quite what
// nsswitch.conf(5) says: the database name ends at a space or colon and
// is compared case-sensitively; the colon is optional; "#" starts no
// comment (a "# ..." line names no database, but words after "#" on a
// real line are services); a service name ends at a space or "[", and
// the bracketed actions are skipped here. A last line without a newline
// is never read: glibc checks for end of file before parsing it.
func parseNsswitch(data []byte) map[string]nsswitchLine {
	out := map[string]nsswitchLine{}
	text := string(data)
	lines := strings.Split(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		l = strings.TrimLeftFunc(l, isSpaceC)
		end := strings.IndexFunc(l, func(r rune) bool { return isSpaceC(r) || r == ':' })
		if end < 0 {
			end = len(l) // glibc sees the newline after the name: no services
		}
		if end == 0 {
			continue // a blank line
		}
		db, rest := l[:end], l[end:]
		rest = strings.TrimLeftFunc(rest, func(r rune) bool { return isSpaceC(r) || r == ':' })
		var services []string
		for rest != "" {
			rest = strings.TrimLeftFunc(rest, isSpaceC)
			n := strings.IndexFunc(rest, func(r rune) bool { return isSpaceC(r) || r == '[' })
			if n < 0 {
				n = len(rest)
			}
			if n == 0 {
				break // "[" with no service before it: glibc stops here
			}
			services = append(services, rest[:n])
			rest = strings.TrimLeftFunc(rest[n:], isSpaceC)
			if strings.HasPrefix(rest, "[") {
				close := strings.IndexByte(rest, ']')
				if close < 0 {
					break
				}
				rest = rest[close+1:]
			}
		}
		out[db] = nsswitchLine{line: i + 1, services: services}
	}
	return out
}

// localSources are the nsswitch services that read the local files: one
// of them on the line keeps the users, groups and passwords in /etc
// reachable. systemd is not one: it synthesizes root and nobody, and
// dynamic and homed users, but does not read /etc/passwd.
var localSources = map[string]bool{"files": true, "compat": true}

// checkNsswitch checks the passwd, group and shadow lines of an
// nsswitch.conf: each must name a local source. A missing line is fine:
// glibc then uses "files" (and for shadow, the passwd line).
func checkNsswitch(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	lines := parseNsswitch(in.data)
	var out []Finding
	for _, db := range []struct {
		name string
		sev  Severity
		text string
	}{
		{"passwd", Blocker, "the users in /etc/passwd cannot be looked up"},
		{"group", Error, "local groups (sudo included) and their members are lost"},
		{"shadow", Error, "local passwords cannot be checked"},
	} {
		l, ok := lines[db.name]
		if !ok {
			continue
		}
		local := false
		for _, s := range l.services {
			local = local || localSources[s]
		}
		if !local {
			out = append(out, Finding{Rule: "nsswitch-no-files", Severity: db.sev, Line: l.line,
				Text: fmt.Sprintf("the %s line names neither files nor compat, so %s", db.name, db.text)})
		}
	}
	return out, nil, nil
}

// checkPreload checks that every library /etc/ld.so.preload names exists.
// The loader (elf/rtld.c) blanks "#" to the end of the line, then splits
// on spaces, tabs, newlines and colons: nothing else, so a carriage
// return is part of the name. A name with a dynamic string token ($LIB,
// $ORIGIN, $PLATFORM) or without a full path is looked up by the loader
// in ways sc cannot repeat: a note, not a finding.
func checkPreload(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	var out []Finding
	var unchecked []string
	seen := map[string]bool{}
	for i, l := range strings.Split(string(in.data), "\n") {
		if hash := strings.IndexByte(l, '#'); hash >= 0 {
			l = l[:hash]
		}
		for _, name := range strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == '\t' || r == ':' }) {
			if seen[name] {
				continue
			}
			seen[name] = true
			text := "preloaded library " + quoteOdd(name) + " does not exist on this machine"
			switch {
			case strings.HasSuffix(name, "\r"):
				// No library is called that, with or without a path.
				text += " (its name ends in a carriage return: a DOS line ending)"
			case strings.Contains(name, "$") || !strings.HasPrefix(name, "/"):
				unchecked = append(unchecked, quoteOdd(name))
				continue
			case c.pathExists(name):
				continue
			}
			out = append(out, Finding{Rule: "preload-missing-lib", Severity: Blocker, Line: i + 1, Text: text})
		}
	}
	var notes []string
	if len(unchecked) > 0 {
		notes = []string{"the loader looks for " + strings.Join(unchecked, ", ") + " itself (no full path, or a $TOKEN); sc did not check it"}
	}
	return out, notes, nil
}

// quoteOdd returns s as it is, or quoted when it holds a character that
// would not print.
func quoteOdd(s string) string {
	if strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}

// checkFlag is for files whose mere existence changes behaviour: the
// finding is there whenever the file is (the checker runs only on a file
// that exists or is being created). The rule comes from the file's name.
func checkFlag(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	switch filepath.Base(in.path) {
	case "nologin":
		return []Finding{{Rule: "flag-nologin", Severity: Warning,
			Text: in.path + " exists, so only root can log in until it is deleted"}}, nil, nil
	case "sshd_not_to_be_run":
		return []Finding{{Rule: "flag-sshd-not-to-be-run", Severity: Warning,
			Text: in.path + " exists, so the SSH server does not start until it is deleted"}}, nil, nil
	}
	return nil, nil, fmt.Errorf("the flag checker has no rule for %s", in.path)
}

// checkHosts checks that /etc/hosts maps localhost to a loopback address.
// glibc (nss/nss_files/files-hosts.c) skips blank and "#" lines, cuts a
// line at "#", splits it on blanks, drops a line whose first field is not
// an address, and compares names case-insensitively.
func checkHosts(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	for _, l := range strings.Split(string(in.data), "\n") {
		if hash := strings.IndexByte(l, '#'); hash >= 0 {
			l = l[:hash]
		}
		f := strings.FieldsFunc(l, isSpaceC)
		if len(f) < 2 {
			continue
		}
		ip := net.ParseIP(f[0])
		if ip == nil || !ip.IsLoopback() {
			continue
		}
		for _, name := range f[1:] {
			if strings.EqualFold(name, "localhost") {
				return nil, nil, nil
			}
		}
	}
	return []Finding{{Rule: "hosts-no-localhost", Severity: Warning,
		Text: "no line maps localhost to 127.0.0.1 or ::1"}}, nil, nil
}

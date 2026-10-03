//go:build linux

package check

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	rules = append(rules,
		Rule{"passwd-root", Blocker, `/etc/passwd has no line glibc can read that gives root uid 0. sudo and
su look root up by name: without that line they fail, and with another
uid they run commands as that uid instead. Ubuntu locks the root
password, so no other way to root is left.
Put the root line back first: root:x:0:0:root:/root:/bin/bash.`},
		Rule{"passwd-invalid", Error, `pwck rejects a line of /etc/passwd. glibc skips a line it cannot read
(an id that is not a number, too few fields), so that user does not
exist: logins, su, sudo and services that run as it fail. A blank or
comment line, a duplicate or bad name, a missing primary group or a
password field other than x is a warning. sc check -v shows pwck's words.`},
		Rule{"passwd-shell-missing", Error, `The login shell of a user does not exist on this machine. login, su and
ssh start that shell, so the user cannot log in (a session already open
keeps going). A missing nologin or false is only a warning: such an
account is not meant to log in. Fix the path: /bin/bash, /bin/sh or
/usr/sbin/nologin; chsh -s does it for one user.`},
		Rule{"group-invalid", Error, `grpck rejects a line of /etc/group. glibc skips a line it cannot read,
so the group does not exist and its members lose it at their next login;
for sudo or admin that ends sudo for everyone (Ubuntu locks root). A blank
or comment line, a duplicate or bad name, a member that is not a user or
a password field other than x is a warning. sc check -v shows grpck's.`},
	)
}

var (
	// The question pwck and grpck answer with No in read-only mode, after
	// the message it belongs to: "delete line '...'? No" quotes the line.
	ckQuestion = regexp.MustCompile(`^(?:delete|add) .*\? No$`)

	pwckBadName = regexp.MustCompile(`^invalid user name '(.*)'(?:: use --badname to ignore)?$`)
	pwckBadUID  = regexp.MustCompile(`^invalid user ID '(\d+)'$`)
	pwckNoGroup = regexp.MustCompile(`^user '(.*)': no group (\d+)$`)
	pwckNoHome  = regexp.MustCompile(`^user '(.*)': directory '(.*)' does not exist$`)
	pwckNoShell = regexp.MustCompile(`^user '(.*)': program '(.*)' does not exist$`)
	pwckNotX    = regexp.MustCompile(`^user (.*) has an entry in \S+, but its password field in \S+ is not set to 'x'$`)
	// What pwck says about the shadow file sc makes up for it.
	pwckShadow = regexp.MustCompile(`^(?:no matching password file entry in .*|invalid shadow password file entry|duplicate shadow password entry|user '.*': last password change in the future)$`)

	grpckBadName = regexp.MustCompile(`^invalid group name '(.*)'$`)
	grpckBadGID  = regexp.MustCompile(`^invalid group ID '(\d+)'$`)
	grpckNoUser  = regexp.MustCompile(`^group (.*): no user (.*)$`)
	grpckNotX    = regexp.MustCompile(`^group (.*) has an entry in \S+, but its password field in \S+ is not set to 'x'$`)
	// What grpck says about the gshadow file sc makes up for it.
	grpckShadow = regexp.MustCompile(`^(?:no matching group file entry in .*|invalid shadow group file entry|duplicate shadow group entry|shadow group .*: no (?:administrative )?user .*|'.*' is a member of the '.*' group in .* but not in .*)$`)
)

// noLoginShells are the shells (by base name) of accounts that are not
// meant to log in, as the scope has them.
var noLoginShells = map[string]bool{"nologin": true, "false": true, "sync": true, "halt": true, "shutdown": true}

// sudoGroups are the groups Ubuntu's /etc/sudoers gives sudo to.
var sudoGroups = map[string]bool{"sudo": true, "admin": true}

// ckSplit returns the lines of data as pwck, grpck and glibc read them
// (fgets): a last line without a newline is a line, the empty string
// after a final newline is not.
func ckSplit(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ckName returns the name pwck and grpck give an entry: its line up to
// the first colon.
func ckName(l string) string {
	name, _, _ := strings.Cut(l, ":")
	return name
}

// glibcID parses an id field as glibc's nss_files does (strtoul, base
// 10): leading blanks and a sign may come first, the digits must end the
// field, and a value of 2^32 or more, or a negative one, is refused.
func glibcID(s string) (uint32, bool) {
	t := strings.TrimLeftFunc(s, isSpaceC)
	neg := false
	if t != "" && (t[0] == '+' || t[0] == '-') {
		neg, t = t[0] == '-', t[1:]
	}
	if t == "" || strings.TrimLeft(t, "0123456789") != "" {
		return 0, false
	}
	var v uint64
	for _, d := range t {
		if v = v*10 + uint64(d-'0'); v > 1<<32-1 {
			return 0, false
		}
	}
	if neg && v != 0 {
		return 0, false
	}
	return uint32(v), true
}

// glibcEntry reads a line of /etc/passwd (7 fields, ids in fields 2 and
// 3) or /etc/group (4 fields, id in field 2) as glibc 2.39's nss_files
// does (nss/nss_files/files-parse.c, checked with fgetpwent and
// fgetgrent on fabricated files): leading blanks are skipped, a blank or
// "#" line is no entry, a NIS name (+ or - first) may leave out the rest,
// every id must be one glibcID accepts, missing fields after the ids are
// empty, and the last field takes the rest of the line, colons included.
// ok is false for a line glibc skips.
func glibcEntry(l string, fields int, ids ...int) (f []string, ok bool) {
	l = strings.TrimLeftFunc(l, isSpaceC)
	if l == "" || l[0] == '#' {
		return nil, false
	}
	f = strings.SplitN(l, ":", fields)
	nis := l[0] == '+' || l[0] == '-'
	for _, i := range ids {
		switch {
		case nis && (i >= len(f) || f[i] == ""):
		case i >= len(f):
			return nil, false
		default:
			if _, ok := glibcID(f[i]); !ok {
				return nil, false
			}
		}
	}
	return f, true
}

// passwdRoot is the passwd-root rule. glibc gives the name root the first
// line it can read with that name, and that line must have uid 0.
func passwdRoot(data []byte) []Finding {
	broken := 0 // the first line named root that glibc cannot read
	for i, l := range ckSplit(data) {
		f, ok := glibcEntry(l, 7, 2, 3)
		if !ok {
			if broken == 0 && ckName(strings.TrimLeftFunc(l, isSpaceC)) == "root" {
				broken = i + 1
			}
			continue
		}
		if f[0] != "root" {
			continue
		}
		if uid, _ := glibcID(f[2]); uid != 0 {
			return []Finding{{Rule: "passwd-root", Severity: Blocker, Line: i + 1, Key: lineKey(data, i+1),
				Text: fmt.Sprintf("the first line for root gives it uid %d, not 0: sudo and su would run as uid %d", uid, uid)}}
		}
		return nil
	}
	if broken > 0 {
		return []Finding{{Rule: "passwd-root", Severity: Blocker, Line: broken, Key: lineKey(data, broken),
			Text: "glibc cannot read root's line, so root does not exist: sudo and su fail"}}
	}
	return []Finding{{Rule: "passwd-root", Severity: Blocker, Text: "no line defines root, so root does not exist: sudo and su fail"}}
}

// ckMessage is one message of pwck or grpck. A question that follows it
// is folded in: the line a "delete line '...'?" question quotes may hold
// anything, so it is kept apart and never reaches a finding.
type ckMessage struct {
	text   string
	quoted string // the line of the file a "delete line" question quotes
	quotes bool
}

func ckMessages(out string) []ckMessage {
	var msgs []ckMessage
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		if ckQuestion.MatchString(l) && len(msgs) > 0 {
			if q, ok := strings.CutPrefix(l, "delete line '"); ok {
				msgs[len(msgs)-1].quoted, msgs[len(msgs)-1].quotes = strings.TrimSuffix(q, "'? No"), true
			}
			continue
		}
		msgs = append(msgs, ckMessage{text: l})
	}
	return msgs
}

// ckLines finds the line of the file a message is about. pwck and grpck
// name a line by its content or by its entry's name, never by number, and
// check the file in order: a search starts at the last message's line.
type ckLines struct {
	lines  []string
	at     int
	quoted map[int]bool // lines a question has quoted: each is quoted once
}

func (s *ckLines) find(match func(i int, l string) bool) int {
	for k := range s.lines {
		i := (s.at + k) % len(s.lines)
		if match(i, s.lines[i]) {
			s.at = i
			return i + 1
		}
	}
	return 0
}

// quote finds the line a question quoted: the same text, or one that
// starts with it (the tool's copy stops at a NUL byte), and not one an
// earlier question quoted (two equal lines are two duplicates).
func (s *ckLines) quote(q string) int {
	if s.quoted == nil {
		s.quoted = map[int]bool{}
	}
	n := s.find(func(i int, l string) bool { return !s.quoted[i] && l == q })
	if n == 0 && q != "" {
		n = s.find(func(i int, l string) bool { return !s.quoted[i] && strings.HasPrefix(l, q) })
	}
	if n > 0 {
		s.quoted[n-1] = true
	}
	return n
}

// id finds the line whose field i is the id v.
func (s *ckLines) id(i int, v string) int {
	return s.find(func(_ int, l string) bool {
		f := strings.Split(l, ":")
		if len(f) <= i {
			return false
		}
		id, ok := glibcID(f[i])
		return ok && fmt.Sprint(id) == v
	})
}

func (s *ckLines) named(name string) int {
	return s.find(func(_ int, l string) bool { return ckName(l) == name })
}

// ckCompanion writes the shadow file pwck (or gshadow for grpck) is
// given, next to the scratch copy: an entry without a password for each
// name of an entry with the right number of fields, NIS lines left out,
// so the two files agree and only the file itself is checked. Without a
// second file pwck reads the real /etc/shadow: a user cannot, and root
// would compare the edited copy with the password hashes, which sc never
// reads.
func ckCompanion(in input, t *ckTool, rest string) (string, error) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, l := range ckSplit(in.data) {
		f := strings.Split(l, ":")
		if len(f) != t.fields || f[0] == "" || f[0][0] == '+' || f[0][0] == '-' || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		b.WriteString(f[0] + rest + "\n")
	}
	p := filepath.Join(filepath.Dir(in.file), t.shadow)
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		return "", fmt.Errorf("write the %s file for %s: %w", t.shadow, t.name, err)
	}
	return p, nil
}

// ckRun runs pwck or grpck in read-only mode, which opens both files
// read-only, locks nothing and writes nothing (checked with strace), and
// returns its messages with the scratch paths turned into the real ones,
// and its exit status: 0 for a clean file, 2 for one with problems.
// Anything else, it did not check the file: ok is false, with a note.
func ckRun(ctx context.Context, c *Checks, in input, tool string, args ...string) (msgs []ckMessage, exit int, notes []string, ok bool, err error) {
	res, notes, ok, err := c.validate(ctx, in, tool, args...)
	if err != nil || !ok {
		return nil, 0, notes, false, err
	}
	real := strings.NewReplacer(filepath.Dir(in.file)+"/", filepath.Dir(in.path)+"/")
	first, _, _ := strings.Cut(strings.TrimSpace(real.Replace(string(res.Err))), "\n")
	if res.Exit != 0 && res.Exit != 2 {
		if first == "" {
			first = fmt.Sprintf("exit %d", res.Exit)
		}
		return nil, res.Exit, append(notes, tool+" could not check the file ("+first+")"), false, nil
	}
	if first != "" {
		notes = append(notes, tool+" printed something sc does not understand ("+first+")")
	}
	return ckMessages(real.Replace(string(res.Out))), res.Exit, notes, true, nil
}

// ckTool is what pwck and grpck differ in: they say the same things about
// their files in nearly the same words.
type ckTool struct {
	name, rule string // pwck, passwd-invalid
	kind, who  string // passwd, user: a passwd entry is a user's
	shadow     string // shadow: the file that holds the passwords
	fields     int    // fields of an entry
	ids        []int  // the fields that hold ids
	invalid    string // the message for a line it cannot read
	dup        string // the message for a name on two lines
	badName    *regexp.Regexp
	badID      *regexp.Regexp
	notX       *regexp.Regexp
	madeUp     *regexp.Regexp // messages about the made-up shadow file
	// more turns a message only this tool prints into findings; known is
	// false for one it does not know either.
	more func(s *ckLines, key func(int) string, t string) (fs []Finding, known bool)
}

var pwckTool = &ckTool{name: "pwck", rule: "passwd-invalid", kind: "passwd", who: "user", shadow: "shadow", fields: 7, ids: []int{2, 3},
	invalid: "invalid password file entry", dup: "duplicate password entry",
	badName: pwckBadName, badID: pwckBadUID, notX: pwckNotX, madeUp: pwckShadow, more: pwckMore}

var grpckTool = &ckTool{name: "grpck", rule: "group-invalid", kind: "group", who: "group", shadow: "gshadow", fields: 4, ids: []int{2},
	invalid: "invalid group file entry", dup: "duplicate group entry",
	badName: grpckBadName, badID: grpckBadGID, notX: grpckNotX, madeUp: grpckShadow, more: grpckMore}

// entry gives the severity and text of a line the tool calls invalid,
// from what glibc makes of it: a blank or comment line it skips, and
// nothing is lost; a line it cannot read it skips too, and the entry is
// gone; any other it reads, but not as written.
func (t *ckTool) entry(l string) (Severity, string) {
	trimmed := strings.TrimLeftFunc(l, isSpaceC)
	name := ckName(trimmed)
	switch _, ok := glibcEntry(l, t.fields, t.ids...); {
	case trimmed == "" || trimmed[0] == '#':
		return Warning, "the line is blank or a comment: glibc skips it, but " + t.name + " rejects it"
	case ok:
		return Error, fmt.Sprintf("the line does not have the %d fields of a %s entry: glibc reads it, but not as written", t.fields, t.kind)
	case t.kind == "passwd":
		return Error, "glibc cannot read the line, so its user does not exist"
	case sudoGroups[name]:
		return Blocker, "glibc cannot read the line of group " + name + ", so its members lose sudo at their next login"
	}
	return Error, "glibc cannot read the line, so its group does not exist: its members lose it at their next login"
}

// findings turns the tool's messages about in into findings. skip is a
// line another rule already reports. A run that exited 2 without a
// message sc understands is not a clean one: a note says so.
func (t *ckTool) findings(in input, msgs []ckMessage, exit, skip int) ([]Finding, []string) {
	lines := ckSplit(in.data)
	s := &ckLines{lines: lines}
	key := func(n int) string { return lineKey(in.data, n) }
	var out []Finding
	add := func(sev Severity, n int, text, raw string) {
		out = append(out, Finding{Rule: t.rule, Severity: sev, Line: n, Text: text, Raw: raw, Key: key(n)})
	}
	said := false            // the tool said something sc understands
	dup := map[string]bool{} // names on an earlier line pwck called a duplicate
	for _, m := range msgs {
		msg := m.text
		if msg == t.name+": no changes" {
			continue // its last line after any message, read-only
		}
		said = true
		switch {
		case msg == t.invalid && m.quotes:
			if n := s.quote(m.quoted); n == 0 || n != skip {
				sev, text := t.entry(m.quoted)
				add(sev, n, text, msg)
			}
		case msg == t.dup && m.quotes:
			// Every line of a name that is on two is named; glibc uses the
			// first, so only the later ones are findings.
			n, name := s.quote(m.quoted), ckName(m.quoted)
			if dup[name] {
				add(Warning, n, fmt.Sprintf("%s %s is defined on an earlier line too: lookups by name use that one, not this one", t.who, quoteOdd(name)), msg)
			}
			dup[name] = true
		case t.badName.MatchString(msg):
			add(Warning, s.named(t.badName.FindStringSubmatch(msg)[1]),
				fmt.Sprintf("%s does not accept the %s name on this line: glibc reads it, but other tools may refuse it", t.name, t.who), msg)
		case t.badID.MatchString(msg):
			id := t.badID.FindStringSubmatch(msg)[1]
			text := fmt.Sprintf("uid %s means no uid to the kernel, so this user cannot log in or run anything", id)
			if t.kind == "group" {
				text = fmt.Sprintf("gid %s means no gid to the kernel, so the members of this group cannot log in", id)
			}
			add(Error, s.id(t.ids[0], id), text, msg)
		case t.notX.MatchString(msg):
			name := t.notX.FindStringSubmatch(msg)[1]
			n := s.named(name)
			text := fmt.Sprintf("the password field of %s %s is not x, so /etc/%s is not used for it", t.who, quoteOdd(name), t.shadow)
			if f := strings.Split(lineAt(lines, n), ":"); t.kind == "passwd" && len(f) > 1 && f[1] == "" {
				// pam_unix takes the field as the hash; empty passes with
				// nullok, which Ubuntu's common-auth sets.
				text = fmt.Sprintf("the password field of user %s is empty, not x: Ubuntu's PAM (nullok) lets it log in without a password", quoteOdd(name))
			}
			add(Warning, n, text, msg)
		case t.madeUp.MatchString(msg):
			// About the shadow file sc made up, not about the file.
		default:
			fs, known := t.more(s, key, msg)
			if !known {
				n := 0
				if m.quotes {
					n = s.quote(m.quoted)
				}
				fs = []Finding{{Rule: t.rule, Severity: Warning, Line: n, Raw: msg, Key: msg,
					Text: t.name + " reports a problem sc has no rule for"}}
			}
			out = append(out, fs...)
		}
	}
	var notes []string
	if exit != 0 && !said {
		notes = []string{fmt.Sprintf("%s exited %d without a message sc understands; the file was not checked", t.name, exit)}
	}
	return out, notes
}

// pwckMore handles what only pwck says: a primary group that does not
// exist (a warning: the user still logs in), a home directory that does
// not exist (left out: a fact about this machine, and a stock Ubuntu has
// several; login then uses /) and a shell that does not exist (the user
// cannot log in).
func pwckMore(s *ckLines, key func(int) string, msg string) ([]Finding, bool) {
	switch {
	case pwckNoGroup.MatchString(msg):
		m := pwckNoGroup.FindStringSubmatch(msg)
		n := s.named(m[1])
		return []Finding{{Rule: "passwd-invalid", Severity: Warning, Line: n, Raw: msg, Key: key(n),
			Text: fmt.Sprintf("the primary group %s of user %s does not exist on this machine", m[2], quoteOdd(m[1]))}}, true
	case pwckNoHome.MatchString(msg):
		return nil, true
	case pwckNoShell.MatchString(msg):
		m := pwckNoShell.FindStringSubmatch(msg)
		user, shell := quoteOdd(m[1]), m[2]
		n := s.named(m[1])
		f := Finding{Rule: "passwd-shell-missing", Severity: Error, Line: n, Raw: msg, Key: key(n),
			Text: fmt.Sprintf("the shell %s of user %s does not exist on this machine, so %s cannot log in", quoteOdd(shell), user, user)}
		if noLoginShells[path.Base(strings.TrimSpace(shell))] {
			f.Severity = Warning
			f.Text = fmt.Sprintf("the shell %s of user %s does not exist on this machine", quoteOdd(shell), user)
		}
		f.Text += dosEnding(shell)
		return []Finding{f}, true
	}
	return nil, false
}

// grpckMore handles what only grpck says: a member that is not a user. It
// keeps the group from nobody, but is likely a typo that keeps it from
// someone. What grpck says of a member glibc does not see on the line (a
// blank before it, an empty one, or one grpck misreads from a line of
// three fields) is left out.
func grpckMore(s *ckLines, key func(int) string, msg string) ([]Finding, bool) {
	m := grpckNoUser.FindStringSubmatch(msg)
	if m == nil {
		return nil, false
	}
	group, member := m[1], m[2]
	n := s.named(group)
	if !glibcMember(lineAt(s.lines, n), member) {
		return nil, true
	}
	return []Finding{{Rule: "group-invalid", Severity: Warning, Line: n, Raw: msg, Key: key(n),
		Text: fmt.Sprintf("member %s of group %s is not a user on this machine%s", quoteOdd(member), quoteOdd(group), dosEnding(member))}}, true
}

// glibcMember reports whether glibc reads name as a member on line l of
// /etc/group: the fourth field split at commas, blanks before each name
// skipped, empty names dropped.
func glibcMember(l, name string) bool {
	f := strings.SplitN(strings.TrimLeftFunc(l, isSpaceC), ":", 4)
	if len(f) < 4 {
		return false
	}
	for _, m := range strings.Split(f[3], ",") {
		if m = strings.TrimLeftFunc(m, isSpaceC); m != "" && m == name {
			return true
		}
	}
	return false
}

// dosEnding is what a text adds when a name ends in a carriage return.
func dosEnding(s string) string {
	if strings.HasSuffix(s, "\r") {
		return " (it ends in a carriage return: a DOS line ending)"
	}
	return ""
}

// lineAt returns line n (from 1) of lines, or "".
func lineAt(lines []string, n int) string {
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// checkPasswd checks /etc/passwd: sc's passwd-root rule, then pwck -r on
// the copy and a made-up shadow file. A broken root line is reported once,
// by passwd-root.
func checkPasswd(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	out := passwdRoot(in.data)
	skip := 0
	if len(out) > 0 {
		skip = out[0].Line
	}
	shadow, err := ckCompanion(in, pwckTool, ":*:::::::")
	if err != nil {
		return out, nil, err
	}
	msgs, exit, notes, ok, err := ckRun(ctx, c, in, "pwck", "-r", in.file, shadow)
	if err != nil || !ok {
		return out, notes, err
	}
	fs, more := pwckTool.findings(in, msgs, exit, skip)
	return append(out, fs...), append(notes, more...), nil
}

// checkGroup checks /etc/group with grpck -r -S on the copy and a made-up
// gshadow file. -S leaves out the remarks on members listed in one file
// and not the other: they would all be about the made-up file.
func checkGroup(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	gshadow, err := ckCompanion(in, grpckTool, ":!::")
	if err != nil {
		return nil, nil, err
	}
	msgs, exit, notes, ok, err := ckRun(ctx, c, in, "grpck", "-r", "-S", in.file, gshadow)
	if err != nil || !ok {
		return nil, notes, err
	}
	fs, more := grpckTool.findings(in, msgs, exit, 0)
	return fs, append(notes, more...), nil
}

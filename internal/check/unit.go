//go:build linux

package check

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func init() {
	rules = append(rules,
		Rule{"unit-syntax", Error, `systemd cannot load the unit, or refuses to start it as written: a
broken section header, a line it cannot accept, a service with no
ExecStart=, or a unit it depends on that is missing or broken.
systemctl start fails until the file is fixed. sc check -v shows the
message from systemd-analyze.`},
		Rule{"unit-exec-missing", Error, `A command on an ExecStart= line, or on ExecStartPre=, ExecStop= or
their kin, does not exist on this machine or is not executable. The
service fails at its next start; one running now keeps going.
Give the full path of an installed program, or install it.`},
		Rule{"unit-notice", Warning, `systemd-analyze has a remark about this line, but the unit loads and
runs: an obsolete setting systemd rewrites (StandardOutput=syslog), one
it will drop (MemoryLimit=), or an unsafe choice (User=nobody).
sc check -v shows the remark; follow it when you can.`},
		Rule{"unit-unknown-key", Warning, `systemd does not know the key, section or value on this line and skips
it: the unit still loads, but the setting has no effect. Most likely a
typo. Check the spelling against the unit's man page (systemd.unit,
systemd.service, systemd.exec, ...).`},
		Rule{"unit-dropin-orphan", Warning, `No unit on this machine has the name of the drop-in's directory (the
unit's name, then .d), so systemd does not read the drop-in and its
settings have no effect. Check the name against systemctl
list-unit-files, or install the unit first.`},
	)
}

// unitSuffixes are the unit types systemd-analyze names in its messages.
const unitSuffixes = `(?:service|socket|device|mount|automount|swap|target|path|timer|slice|scope)`

var (
	// A message about a line of a file, and one about a unit by name.
	unitFileMsg = regexp.MustCompile(`^(/.*?):(\d+): (.*)$`)
	unitNameMsg = regexp.MustCompile(`^([^\s:/]+\.` + unitSuffixes + `): (.*)$`)
	// The summary lines after a unit that did not load, or could not be
	// found at all.
	unitSummary  = regexp.MustCompile(`^Unit (\S+\.` + unitSuffixes + `) (.*)$`)
	unitExec     = regexp.MustCompile(`^Command (\S+) is not executable: (.*)$`)
	unitStart    = regexp.MustCompile(`^Failed to create \S+/start: (.*)$`)
	unitDep      = regexp.MustCompile(`^Unit (\S+) (not found|has a bad unit file setting)\.?$`)
	unitSocket   = regexp.MustCompile(`^service (\S+) not loaded, socket cannot be started\.$`)
	unitKeyName  = regexp.MustCompile(`^Unknown key name '([^']*)' in section '([^']*)'`)
	unitSection  = regexp.MustCompile(`^Unknown section '([^']*)'`)
	unitKeyInMsg = regexp.MustCompile(`\b([A-Z][A-Za-z]*)=`)
)

// unitName returns the names systemd-analyze verify calls the unit in
// file: its file name, and for a template (my@.service) the instance it
// verifies instead (my@i.service).
func unitName(file string) (name, instance string) {
	name = filepath.Base(file)
	instance = name
	if i := strings.Index(name, "@."); i >= 0 {
		instance = name[:i] + "@i." + name[i+2:]
	}
	return name, instance
}

// unitLine returns the number of the first line of data whose key is key,
// or 0. ExecStart and friends are also matched by their command: the
// message names the command, not the line.
func unitLine(data []byte, key, command string) int {
	for i, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if key != "" && k == key {
			return i + 1
		}
		if command != "" && strings.HasPrefix(k, "Exec") {
			// Prefixes before the command: "-", "@", ":", "+", "!", "!!".
			f := strings.Fields(strings.TrimLeft(strings.TrimSpace(v), "-@:+!"))
			if len(f) > 0 && f[0] == command {
				return i + 1
			}
		}
	}
	return 0
}

// unitKey returns the key of line n of data, or "".
func unitKey(data []byte, n int) string {
	lines := strings.Split(string(data), "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	k, _, _ := strings.Cut(lines[n-1], "=")
	return strings.TrimSpace(k)
}

// checkUnit checks a systemd unit with systemd-analyze verify, which loads
// the scratch copy under the unit's own name together with everything it
// depends on (plan A6). Only what it says about this unit counts: a
// dependency's problems are its own file's, and a failed man page lookup
// for Documentation= is no problem with the unit.
func checkUnit(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	// --man=no: a Documentation=man: entry would run man (and groff) on
	// the page it names, as the caller, root under scd.
	res, notes, ok, err := c.validate(ctx, in, "systemd-analyze", "verify", "--man=no", in.file)
	if err != nil || !ok {
		return nil, notes, err
	}
	name, instance := unitName(in.file)
	out, problem, said, _ := readVerify(res, in.data, in.file, in.path, name, instance)
	if problem != "" {
		return nil, append(notes, in.say(problem, said)), nil
	}
	return out, notes, nil
}

// unitDirs is the system unit path, in systemd's order; tests fake it.
var unitDirs = []string{"/etc/systemd/system", "/run/systemd/system", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}

// unitAlias returns the unit that name is an alias of (sshd.service of
// ssh.service on Ubuntu): the first file of that name on the unit path is
// a symlink to a unit of another name. systemd says what it finds under
// that name. "" when name is no alias.
func unitAlias(name string) string {
	for _, d := range unitDirs {
		p := filepath.Join(d, name)
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return ""
		}
		// A link to /dev/null masks the unit; one to a file of the same
		// name (systemctl link) is no alias.
		t, err := filepath.EvalSymlinks(p)
		if b := filepath.Base(t); err == nil && b != name && filepath.Ext(b) == filepath.Ext(name) {
			return b
		}
		return ""
	}
	return ""
}

// unitNames returns the names systemd-analyze verify may give unit in its
// messages: its own and the instance it verifies of a template, and those
// of the unit it is an alias of, whole or by its template (autovt@.service
// is getty@.service on Ubuntu: a drop-in for autovt@tty2.service is
// reported under getty@tty2.service; review of chunk F, A1).
func unitNames(unit string) []string {
	name, instance := unitName(unit)
	names := []string{name, instance}
	if a := unitAlias(name); a != "" {
		_, ai := unitName(a)
		names = append(names, a, ai)
	}
	if at := strings.IndexByte(name, '@'); at > 0 && !strings.Contains(name, "@.") {
		if a := unitAlias(name[:at+1] + filepath.Ext(name)); strings.Contains(a, "@.") {
			names = append(names, a[:strings.IndexByte(a, '@')+1]+name[at+1:])
		}
	}
	return names
}

// generatorDirs are where systemd's generators put the units they make at
// boot (an /etc/init.d script, an fstab line), which systemd-analyze
// verify does not run; tests fake them.
var generatorDirs = []string{"/run/systemd/generator.early", "/run/systemd/generator", "/run/systemd/generator.late"}

// unitGenerated reports whether a generator made a unit of one of names.
func unitGenerated(names []string) bool {
	for _, d := range generatorDirs {
		for _, n := range names {
			if _, err := os.Lstat(filepath.Join(d, n)); err == nil {
				return true
			}
		}
	}
	return false
}

// checkUnitDropIn checks a drop-in, /etc/systemd/system/NAME.d/X.conf,
// together with the unit NAME it changes (M3 follow-up 1). systemd-analyze
// verify loads the unit by name with a scratch directory first on its
// unit path (SYSTEMD_UNIT_PATH; the trailing ":" keeps the stock path), so
// the candidate there takes the place of the drop-in of the same name and
// comes with the unit's other drop-ins. What it says about the drop-in's
// lines is the drop-in's. What it says about the unit is the drop-in's
// only when a second run, with that file empty, does not say it too: a
// refusal the drop-in causes (a second ExecStart= without the empty one
// before it), not a problem the unit had before.
func checkUnitDropIn(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	dir := filepath.Base(filepath.Dir(in.path))
	unit := strings.TrimSuffix(dir, ".d")
	dot := strings.LastIndexByte(unit, '.')
	if unit == dir || dot <= 0 {
		return nil, []string{in.skip("the drop-in is not in a unit's NAME.d directory; it was not checked")}, nil
	}
	if stem := unit[:dot]; stem != "-" && strings.HasSuffix(stem, "-") {
		// systemd 246 and later: one drop-in for every unit whose name
		// starts with stem. Which of them it breaks, sc does not judge.
		// -.mount.d is the root mount's own (A5).
		return nil, []string{in.skip("a drop-in for every unit whose name starts with " + stem + "; it was not checked")}, nil
	}
	if strings.ContainsRune(filepath.Dir(in.file), ':') {
		// SYSTEMD_UNIT_PATH is split on ":" (A6).
		return nil, []string{in.skip("sc's scratch directory has a \":\" in its path, which systemd's unit path cannot hold; the drop-in was not checked")}, nil
	}
	_, instance := unitName(unit)
	names := unitNames(unit)
	verify := func(sub string, data []byte) (res Result, file string, notes []string, ok bool, err error) {
		root := filepath.Join(filepath.Dir(in.file), sub)
		file = filepath.Join(root, dir, filepath.Base(in.path))
		if err := scratchMkdir(filepath.Dir(file)); err != nil {
			return res, file, nil, false, err
		}
		if err := os.WriteFile(file, data, 0o600); err != nil {
			return res, file, nil, false, err
		}
		res, notes, ok, err = c.validateEnv(ctx, in, []string{"SYSTEMD_UNIT_PATH=" + root + ":"}, "systemd-analyze", "verify", "--man=no", instance)
		return res, file, notes, ok, err
	}
	res, file, notes, ok, err := verify("with", in.data)
	if err != nil || !ok {
		return nil, notes, err
	}
	out, problem, said, notFound := readVerify(res, in.data, file, in.path, names...)
	switch {
	case notFound && unitGenerated(names):
		// A4: on the machine all the same.
		return nil, append(notes, in.skip(unit+" is made at boot by a generator, which systemd-analyze does not run; the drop-in was not checked")), nil
	case notFound:
		return []Finding{{Rule: "unit-dropin-orphan", Severity: Warning, Raw: said,
			Text: unit + " is not on this machine, so systemd does not read the drop-in"}}, notes, nil
	case problem != "":
		return nil, append(notes, in.say(problem, said)), nil
	}
	own := func(f Finding) bool { return strings.HasPrefix(f.Raw, in.path+":") }
	if !slices.ContainsFunc(out, func(f Finding) bool { return !own(f) }) {
		return out, notes, nil // only the drop-in's lines: no second run
	}
	res, file, _, ok, err = verify("without", nil)
	if err != nil {
		return nil, nil, err
	}
	var before []Finding
	if ok {
		before, _, _, _ = readVerify(res, in.data, file, in.path, names...)
	} else {
		// No baseline: what is said about the unit is not known to be
		// the drop-in's doing.
		notes = append(notes, "systemd-analyze did not finish a second run, without the drop-in; what it said about "+unit+" as a whole is left out")
		before = slices.DeleteFunc(slices.Clone(out), own)
	}
	key := func(f Finding) string { return f.Rule + "\x00" + f.Text + "\x00" + f.Key }
	had := map[string]bool{}
	for _, f := range before {
		had[key(f)] = true
	}
	return slices.DeleteFunc(out, func(f Finding) bool { return !own(f) && had[key(f)] }), notes, nil
}

// readVerify reads what systemd-analyze verify said in res about the unit
// called one of names (its name, a template's instance, the unit it is an
// alias of), read from file, which is path on the machine; data is that
// file's content. problem, when set, is why it says nothing about the
// unit, as a note, and said the line of verify's behind it; notFound is
// set when that line says no unit has the name.
func readVerify(res Result, data []byte, file, path string, names ...string) (out []Finding, problem, said string, notFound bool) {
	syntax := -1        // index in out of the unit-syntax finding the summary lines belong to
	recognized := false // a line about some unit was read
	var unknown []string
	var remarks []int // indexes in out of unit-notice findings
	add := func(f Finding) {
		out = append(out, f)
		if f.Rule == "unit-syntax" {
			syntax = len(out) - 1
		}
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(string(res.Err), "\n") {
		// A line said twice is one problem: a template's drop-in is read
		// for the template and again for its instance.
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		// The scratch path is the real one from here on: findings never
		// carry it, and nothing but this file can be at that path.
		raw := strings.ReplaceAll(l, file, path)
		switch {
		case strings.HasPrefix(raw, path+":"):
			m := unitFileMsg.FindStringSubmatch(raw)
			if m == nil {
				unknown = append(unknown, raw)
				continue
			}
			recognized = true
			n, _ := strconv.Atoi(m[2])
			msg := m[3]
			f := Finding{Line: n, Raw: raw}
			key := unitKey(data, n)
			if strings.Contains(msg, "gnoring") || strings.Contains(msg, "gnored") {
				// The line is skipped; the unit loads without it.
				f.Rule, f.Severity = "unit-unknown-key", Warning
				switch {
				case unitKeyName.MatchString(msg):
					k := unitKeyName.FindStringSubmatch(msg)
					f.Text = fmt.Sprintf("%s= is not a key systemd knows in [%s]; the line is ignored", k[1], k[2])
				case unitSection.MatchString(msg):
					f.Text = fmt.Sprintf("[%s] is not a section systemd knows; it is ignored", unitSection.FindStringSubmatch(msg)[1])
				case strings.HasPrefix(msg, "Failed to parse") && key != "":
					f.Text = fmt.Sprintf("the value of %s= is not one systemd accepts; the line is ignored", key)
				case strings.HasPrefix(msg, "Assignment outside of section"):
					f.Text = "the line is outside any [Section]; it is ignored"
				case strings.HasPrefix(msg, "Missing '='"):
					f.Text = "the line is not Key=value; it is ignored"
				default:
					f.Text, f.Key = "systemd ignores the line", msg
				}
				// The line's content in the key: a second Restrat= added
				// above an old one is blamed for the new line.
				f.Key = lineKey(data, n) + " " + f.Key
			} else if strings.HasPrefix(msg, "Invalid section header") {
				f.Rule, f.Severity = "unit-syntax", Error
				f.Text = "the section header is not valid; the unit does not load"
			} else {
				// A remark (an obsolete or unsafe setting) unless the unit
				// then fails to load, which only the summary line says.
				f.Rule, f.Severity, f.Text, f.Key = "unit-notice", Warning, "systemd has a remark about the line; the unit loads", msg
				remarks = append(remarks, len(out))
			}
			add(f)
		case unitNameMsg.MatchString(raw):
			m := unitNameMsg.FindStringSubmatch(raw)
			recognized = true
			if !slices.Contains(names, m[1]) {
				continue // another unit's problem
			}
			msg := m[2]
			f := Finding{Rule: "unit-syntax", Severity: Error, Raw: raw}
			switch {
			case unitExec.MatchString(msg):
				e := unitExec.FindStringSubmatch(msg)
				f.Rule, f.Line = "unit-exec-missing", unitLine(data, "", e[1])
				if strings.HasPrefix(e[2], "No such file") {
					f.Text = e[1] + " does not exist on this machine"
				} else {
					f.Text = e[1] + " is not executable (" + strings.ToLower(e[2]) + ")"
				}
			case strings.HasPrefix(msg, "Command 'man "):
				continue // Documentation= names a man page that is not installed: no problem with the unit
			case strings.HasPrefix(msg, "Failed to open "):
				// It never read the file; "Unit NAME not found." follows.
				return nil, "systemd-analyze could not load the unit", raw, false
			case unitStart.MatchString(msg):
				reason := unitStart.FindStringSubmatch(msg)[1]
				if d := unitDep.FindStringSubmatch(reason); d != nil && d[2] == "not found" {
					f.Text = d[1] + ", which the unit depends on, does not exist on this machine"
				} else if d != nil {
					f.Text = d[1] + ", which the unit depends on, has a bad unit file"
				} else {
					f.Text, f.Key = "systemd cannot start the unit", reason
				}
			case unitSocket.MatchString(msg):
				f.Text = "the socket's service " + unitSocket.FindStringSubmatch(msg)[1] + " does not exist on this machine"
			case strings.HasSuffix(msg, " Refusing."):
				msg = strings.TrimSuffix(strings.TrimSuffix(msg, " Refusing."), ".")
				key := ""
				if k := unitKeyInMsg.FindStringSubmatch(msg); k != nil {
					key = k[1]
					f.Line = unitLine(data, key, "")
				}
				switch {
				case strings.HasPrefix(msg, "Service has no ExecStart="):
					f.Text = "the service has no ExecStart= line"
				case strings.HasSuffix(msg, "= setting is missing"):
					f.Text = "the " + key + "= line is missing"
				case strings.HasSuffix(msg, "= setting doesn't match unit name"):
					f.Text = "the " + key + "= line does not match the unit's name"
				case strings.HasPrefix(msg, "Service has more than one ExecStart="):
					f.Text = "more than one ExecStart= line, which only Type=oneshot allows"
				case strings.HasPrefix(msg, "Timer unit lacks value setting"):
					f.Text = "the timer has no valid OnCalendar= or other On...= line"
				default:
					f.Text, f.Key = "systemd refuses the unit as written", msg
				}
			default:
				// Something verify learns to say later: the unit loads
				// (its load failures all say so), so a warning.
				f.Severity, f.Text, f.Key = Warning, "systemd-analyze reports a problem sc has no rule for", msg
			}
			add(f)
		case unitSummary.MatchString(raw):
			m := unitSummary.FindStringSubmatch(raw)
			recognized = true
			if !slices.Contains(names, m[1]) {
				continue
			}
			msg := m[2]
			switch {
			case strings.HasPrefix(msg, "not found"):
				// It never loaded the file (unreadable, or a name it rejects),
				// or no unit has the name.
				return nil, "systemd-analyze could not load the unit", raw, true
			case strings.HasPrefix(msg, "failed to load properly") && len(remarks) > 0 && syntax < 0:
				// It did not load: its remarks were the reasons.
				for _, i := range remarks {
					out[i].Rule, out[i].Severity, out[i].Text = "unit-syntax", Error, "systemd cannot read the line; the unit does not load"
				}
				out[remarks[len(remarks)-1]].Raw += "; " + raw
				syntax = remarks[len(remarks)-1]
			case syntax >= 0:
				out[syntax].Raw += "; " + raw
			case strings.HasPrefix(msg, "is masked"):
				f := Finding{Rule: "unit-syntax", Severity: Error, Raw: raw, Text: "the unit is masked and cannot be started"}
				if len(strings.TrimSpace(string(data))) == 0 {
					f.Text = "the file is empty, so the unit is masked and cannot be started"
				}
				add(f)
			default:
				add(Finding{Rule: "unit-syntax", Severity: Error, Raw: raw, Key: msg, Text: "systemd cannot load the unit"})
			}
		case unitFileMsg.MatchString(raw):
			recognized = true // a line of another unit's file
		default:
			unknown = append(unknown, raw)
		}
	}
	if res.Exit != 0 && !recognized && len(out) == 0 {
		// It said nothing about any unit: it did not get as far as the file.
		if len(unknown) > 0 {
			said = unknown[0]
		}
		return nil, fmt.Sprintf("systemd-analyze could not check the unit (exit %d)", res.Exit), said, false
	}
	return out, "", "", false
}

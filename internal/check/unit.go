//go:build linux

package check

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
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
		Rule{"unit-unknown-key", Warning, `systemd does not know the key, section or value on this line and skips
it: the unit still loads, but the setting has no effect. Most likely a
typo. Check the spelling against the unit's man page (systemd.unit,
systemd.service, systemd.exec, ...).`},
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
	res, notes, ok, err := c.validate(ctx, in, "systemd-analyze", "verify", in.file)
	if err != nil || !ok {
		return nil, notes, err
	}
	name, instance := unitName(in.file)
	var out []Finding
	syntax := -1        // index in out of the unit-syntax finding the summary lines belong to
	recognized := false // a line about some unit was read
	var unknown []string
	add := func(f Finding) {
		out = append(out, f)
		if f.Rule == "unit-syntax" {
			syntax = len(out) - 1
		}
	}
	for _, l := range strings.Split(string(res.Err), "\n") {
		if l == "" {
			continue
		}
		// The scratch path is the real one from here on: findings never
		// carry it, and nothing but this file can be at that path.
		raw := strings.ReplaceAll(l, in.file, in.path)
		switch {
		case strings.HasPrefix(raw, in.path+":"):
			m := unitFileMsg.FindStringSubmatch(raw)
			if m == nil {
				unknown = append(unknown, raw)
				continue
			}
			recognized = true
			n, _ := strconv.Atoi(m[2])
			msg := m[3]
			f := Finding{Line: n, Raw: raw}
			if strings.Contains(msg, "gnoring") {
				// The line is skipped; the unit loads without it.
				f.Rule, f.Severity = "unit-unknown-key", Warning
				switch {
				case unitKeyName.MatchString(msg):
					k := unitKeyName.FindStringSubmatch(msg)
					f.Text = fmt.Sprintf("%s= is not a key systemd knows in [%s]; the line is ignored", k[1], k[2])
				case unitSection.MatchString(msg):
					f.Text = fmt.Sprintf("[%s] is not a section systemd knows; it is ignored", unitSection.FindStringSubmatch(msg)[1])
				case strings.HasPrefix(msg, "Failed to parse") && unitKey(in.data, n) != "":
					f.Text = fmt.Sprintf("the value of %s= is not one systemd accepts; the line is ignored", unitKey(in.data, n))
				case strings.HasPrefix(msg, "Assignment outside of section"):
					f.Text = "the line is outside any [Section]; it is ignored"
				case strings.HasPrefix(msg, "Missing '='"):
					f.Text = "the line is not Key=value; it is ignored"
				default:
					f.Text, f.Key = "systemd ignores the line", msg
				}
			} else {
				f.Rule, f.Severity = "unit-syntax", Error
				if strings.HasPrefix(msg, "Invalid section header") {
					f.Text = "the section header is not valid; the unit does not load"
				} else {
					f.Text, f.Key = "systemd cannot read the line; the unit does not load", msg
				}
			}
			add(f)
		case unitNameMsg.MatchString(raw):
			m := unitNameMsg.FindStringSubmatch(raw)
			recognized = true
			if m[1] != name && m[1] != instance {
				continue // another unit's problem
			}
			msg := m[2]
			f := Finding{Rule: "unit-syntax", Severity: Error, Raw: raw}
			switch {
			case unitExec.MatchString(msg):
				e := unitExec.FindStringSubmatch(msg)
				f.Rule, f.Line = "unit-exec-missing", unitLine(in.data, "", e[1])
				if strings.HasPrefix(e[2], "No such file") {
					f.Text = e[1] + " does not exist on this machine"
				} else {
					f.Text = e[1] + " is not executable (" + strings.ToLower(e[2]) + ")"
				}
			case strings.HasPrefix(msg, "Command 'man "):
				continue // Documentation= names a man page that is not installed: no problem with the unit
			case strings.HasPrefix(msg, "Failed to open "):
				// It never read the file; "Unit NAME not found." follows.
				return nil, append(notes, "systemd-analyze could not load the unit ("+raw+")"), nil
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
					f.Line = unitLine(in.data, key, "")
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
			if m[1] != name && m[1] != instance {
				continue
			}
			msg := m[2]
			switch {
			case strings.HasPrefix(msg, "not found"):
				// It never loaded the file (unreadable, or a name it rejects).
				return nil, append(notes, "systemd-analyze could not load the unit ("+raw+")"), nil
			case syntax >= 0:
				out[syntax].Raw += "; " + raw
			case strings.HasPrefix(msg, "is masked"):
				f := Finding{Rule: "unit-syntax", Severity: Error, Raw: raw, Text: "the unit is masked and cannot be started"}
				if len(strings.TrimSpace(string(in.data))) == 0 {
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
		msg := fmt.Sprintf("exit %d", res.Exit)
		if len(unknown) > 0 {
			msg = unknown[0]
		}
		return nil, append(notes, "systemd-analyze could not check the unit ("+msg+")"), nil
	}
	return out, notes, nil
}

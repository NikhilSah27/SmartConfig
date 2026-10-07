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
		Rule{"udev-invalid", Error, `udev cannot use this line, or a key on it, and leaves it out: the rule
does not rename the network interface, set the device's owner or mode,
or run the program it was written for. udev only logs this, so nothing
else shows the mistake. Fix the key, operator or value sc check -v
names; udevadm verify checks the file again.`},
		Rule{"udev-notice", Warning, `udevadm warns about this line, which udev still reads, though maybe not
as meant: an operator it corrects (NAME+= read as NAME=), a condition
written twice, a line with conditions only and nothing to do, a second
GOTO or an OPTIONS value it ignores, or a sysfs path a later kernel may
not have. sc check -v shows the warning; follow it when you can.`},
	)
}

var (
	// udevadm verify's message about a line: "FILE:LINE MSG". For a line
	// continued with backslashes it names the last one.
	udevMsg = regexp.MustCompile(`^(/.*?):(\d+) (.*)$`)
	// Its summary, on stdout.
	udevChecked = regexp.MustCompile(`(?m)^(\d+) udev rules files have been checked\.$`)
	// The messages udev logs as warnings, not errors: it still reads the
	// line. Each was seen at that level from systemd 255 (SYSTEMD_COLORS=1
	// colours a message by its level).
	udevWarning = regexp.MustCompile(`^(duplicate expressions\.|\S+ key takes .* operator, assuming '='\.|` +
		`The line has no effect(, ignoring| any more, dropping)\.|Contains multiple (GOTO|LABEL) keys, ignoring .*|` +
		`Invalid value for OPTIONS key, ignoring: .*|Reordering RESULT check after PROGRAM assignment\.|` +
		`.* may (not be available in|break in) future kernels\.|".*" must be specified as "subsystem"\.)$`)

	udevKey      = regexp.MustCompile(`^Invalid key '([^']*)'\.$`)
	udevOp       = regexp.MustCompile(`^Invalid (operator|attribute) for (\S+)\.$`)
	udevValue    = regexp.MustCompile(`^Invalid value ".*" for (\S+) \(char \d+: (.*)\), ignoring\.$`)
	udevName     = regexp.MustCompile(`^Unknown (user|group) '([^']*)', ignoring\.$`)
	udevGoto     = regexp.MustCompile(`^GOTO="([^"]*)" has no matching label, ignoring\.$`)
	udevAssuming = regexp.MustCompile(`^(\S+) key takes .* operator, assuming '='\.$`)
	udevOldName  = regexp.MustCompile(`^"(.*)" must be specified as "subsystem"\.$`)
)

// udevText turns udevadm's message about a line into a rule and a
// sentence of ours. The severity follows udev's own level: at error it
// leaves the line, or a key on it, out; at warning it still reads the
// line. A message sc does not know is an error, as verify failed.
func udevText(msg string) (rule string, sev Severity, text string) {
	if udevWarning.MatchString(msg) {
		switch {
		case msg == "duplicate expressions.":
			text = "the line has the same condition twice"
		case udevAssuming.MatchString(msg):
			text = udevAssuming.FindStringSubmatch(msg)[1] + " does not take this operator; udev reads it as ="
		case strings.HasPrefix(msg, "The line has no effect, "):
			text = "the line has conditions only and does nothing"
		case strings.HasPrefix(msg, "The line has no effect any more"):
			text = "nothing on the line has an effect any more; udev drops it"
		case strings.HasPrefix(msg, "Contains multiple GOTO"):
			text = "the line has more than one GOTO; udev ignores all but one"
		case strings.HasPrefix(msg, "Contains multiple LABEL"):
			text = "the line has more than one LABEL; udev ignores all but one"
		case strings.HasPrefix(msg, "Invalid value for OPTIONS"):
			text = "udev does not know a value of OPTIONS on the line and ignores it"
		case strings.HasPrefix(msg, "Reordering "):
			text = "RESULT comes before PROGRAM on the line; udev checks it after"
		case udevOldName.MatchString(msg):
			text = fmt.Sprintf("%q is an old name for \"subsystem\"; udev reads it as that", udevOldName.FindStringSubmatch(msg)[1])
		default:
			text = "the line reads a sysfs path that a later kernel may not have"
		}
		return "udev-notice", Warning, text
	}
	switch {
	case udevKey.MatchString(msg):
		text = "udev does not know the key " + udevKey.FindStringSubmatch(msg)[1] + "; it skips the line"
	case udevOp.MatchString(msg):
		m := udevOp.FindStringSubmatch(msg)
		if m[1] == "operator" {
			text = m[2] + " does not take this operator (== compares, = assigns); udev skips the line"
		} else {
			text = m[2] + " has an {attribute} it does not take, or lacks one it needs; udev skips the line"
		}
	case msg == "Invalid key/value pair, ignoring.":
		text = `the line is not a list of KEY=="value" pairs (a quote or comma may be missing); udev skips it`
	case udevValue.MatchString(msg):
		m := udevValue.FindStringSubmatch(msg)
		text = fmt.Sprintf("the value of %s is not valid (%s); udev skips the line", m[1], m[2])
	case udevName.MatchString(msg):
		m := udevName.FindStringSubmatch(msg)
		text = fmt.Sprintf("%s %q does not exist on this machine; udev ignores the setting", m[1], m[2])
	case udevGoto.MatchString(msg):
		text = fmt.Sprintf("no LABEL=%q follows this GOTO in the file; udev ignores the GOTO", udevGoto.FindStringSubmatch(msg)[1])
	case strings.HasPrefix(msg, "conflicting match expressions"):
		text = "the line's conditions contradict each other, so it never applies"
	case strings.HasPrefix(msg, "Unexpected EOF after line continuation"):
		text = "the file ends in a line continued with a backslash; udev skips that line"
	case strings.HasPrefix(msg, "Ignoring NAME="):
		text = "NAME= here would change nothing; udev ignores it"
	case strings.HasPrefix(msg, "Line is too long"):
		text = "the line is too long; udev skips it"
	default:
		text = "udev does not accept the line as written"
	}
	return "udev-invalid", Error, text
}

// checkUdev checks a rules file with udevadm verify, which parses it as
// systemd-udevd does, on its own: a GOTO and its LABEL are in one file.
// --no-style leaves out remarks on spacing and commas: udev reads such a
// line the same, and every stock rules file on the dev VM that fails
// verify (8 of 120) fails for style alone. User and group names are
// looked up on this machine, as udevd does when it loads the rules; for a
// saved version that is what restoring it would give.
func checkUdev(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	res, notes, ok, err := c.validate(ctx, in, "udevadm", "verify", "--no-style", in.file)
	if err != nil || !ok {
		return nil, notes, err
	}
	var out []Finding
	byLine := map[int]int{} // line -> index in out of its first finding
	var unknown []string
	for _, l := range strings.Split(string(res.Err), "\n") {
		// The scratch path is the real one from here on.
		raw := strings.ReplaceAll(l, in.file, in.path)
		if l == "" || raw == in.path+": udev rules check failed." || raw == in.path+": udev rules have style issues." {
			continue
		}
		if reason, isParse := strings.CutPrefix(raw, "Failed to parse rules file "+in.path+": "); isParse {
			if reason != "No buffer space available" {
				return nil, append(notes, in.say("udevadm could not read the file", raw)), nil
			}
			// A line longer than udev reads (16 KiB): udevd skips the file.
			out = append(out, Finding{Rule: "udev-invalid", Severity: Error, Raw: raw, Key: reason,
				Text: "a line is longer than udev reads, so udev skips the whole file"})
			continue
		}
		m := udevMsg.FindStringSubmatch(raw)
		if m == nil || m[1] != in.path {
			unknown = append(unknown, raw)
			continue
		}
		n, _ := strconv.Atoi(m[2])
		msg := m[3]
		if strings.HasPrefix(msg, "style: ") {
			continue
		}
		if i, seen := byLine[n]; seen && strings.HasPrefix(msg, "The line has no effect") {
			// What a message before it did to the line: nothing is left.
			out[i].Raw += "; " + raw
			continue
		}
		rule, sev, text := udevText(msg)
		if _, seen := byLine[n]; !seen {
			byLine[n] = len(out)
		}
		out = append(out, Finding{Rule: rule, Severity: sev, Line: n, Text: text, Raw: raw, Key: lineKey(in.data, n) + " " + msg})
	}
	switch {
	case len(out) > 0:
	case res.Exit != 0:
		note := fmt.Sprintf("udevadm could not check the file (exit %d)", res.Exit)
		if len(unknown) > 0 {
			note = in.say(note, unknown[0])
		}
		return nil, append(notes, note), nil
	case udevChecked.FindSubmatch(res.Out) == nil || string(udevChecked.FindSubmatch(res.Out)[1]) != "1":
		// It counts the files it checked: 0 for one it passed over.
		return nil, append(notes, "udevadm verify did not report checking the file; it was not checked"), nil
	}
	return out, notes, nil
}

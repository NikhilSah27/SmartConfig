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
		Rule{"grub-default-syntax", Blocker, `update-grub reads /etc/default/grub, then grub.d/*.cfg there, as shell,
and this line is not valid shell: update-grub stops with an error, or
writes a grub.cfg with the wrong settings, which shows at the next boot.
A value with spaces needs double quotes, and = has no spaces around it:
update-grub would run the rest as a command. A line is NAME="value".`},
		Rule{"grubcfg-syntax", Blocker, `GRUB cannot read its menu past this line: at the next boot it prints an
error and drops to the grub> prompt instead of booting.
grub.cfg is generated: fix /etc/default/grub or /etc/grub.d and run
update-grub rather than editing it. custom.cfg is yours to fix.`},
	)
}

var shSyntax = regexp.MustCompile(`^(.*): (\d+): Syntax error: (.*)$`)

// checkShSyntax checks /etc/default/grub and /etc/default/grub.d/*.cfg,
// shell fragments that update-grub sources, with sh -n (dash on Ubuntu),
// which stops at the first error.
func checkShSyntax(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	own := shAssignments(in.data)
	res, notes, ok, err := c.validate(ctx, in, "sh", "-n", in.file)
	if err != nil || !ok {
		return own, notes, err
	}
	out := own
	for _, l := range strings.Split(string(res.Err), "\n") {
		if l == "" {
			continue
		}
		m := shSyntax.FindStringSubmatch(l)
		if m == nil || m[1] != in.file {
			// "sh: 0: cannot open ...", or anything else about itself.
			return out, append(notes, in.say("sh could not check the file", strings.ReplaceAll(l, in.file, in.path))), nil
		}
		n, _ := strconv.Atoi(m[2])
		text := fmt.Sprintf("the line is not valid shell (%s)", m[3])
		// dash says so at the end of the file, past its last line: the
		// line is the one the quote opens on (M3 follow-up 8).
		if strings.HasPrefix(m[3], "Unterminated quoted string") {
			if open := shQuoteOpen(in.data); open > 0 {
				n, text = open, "a quote on this line is never closed"
			}
		}
		n = min(n, max(1, strings.Count(strings.TrimSuffix(string(in.data), "\n"), "\n")+1))
		out = append(out, Finding{Rule: "grub-default-syntax", Severity: Blocker, Line: n, Key: lineKey(in.data, n),
			Raw: strings.ReplaceAll(l, in.file, in.path), Text: text})
	}
	if res.Exit != 0 && len(out) == 0 {
		return nil, append(notes, fmt.Sprintf("sh could not check the file (exit %d, no message)", res.Exit)), nil
	}
	return out, notes, nil
}

// shQuoteOpen returns the line of a quote the file never closes, or 0. In
// a file of NAME="value" lines it is the first line that leaves a quote
// open read on its own, unless the next such line closes it (a value over
// several lines): read as one file, the next line's first quote would
// close the open one and its second open another, and the mistake would
// seem to be a line later. A file whose every line is closed read on its
// own is closed read as a whole.
func shQuoteOpen(data []byte) int {
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		if !shOpen(lines[i]) {
			continue
		}
		j := i + 1
		for j < len(lines) && !shOpen(lines[j]) {
			j++
		}
		if j == len(lines) || shOpen(strings.Join(lines[i:j+1], "\n")) {
			return i + 1
		}
		i = j // a value over lines i+1 to j+1
	}
	return 0
}

// shOpen reports whether text leaves a quote open, read as sh reads it: a
// ', ", or ` quote, a backslash outside single quotes, and a # that starts
// a word begins a comment.
func shOpen(text string) bool {
	var quote byte
	word := true // at the start of a word
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			}
		case ch == '\\' && i+1 < len(text):
			i++ // the next one is taken as it is, a newline too
			word = false
			continue
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '#' && word:
			for i+1 < len(text) && text[i+1] != '\n' {
				i++
			}
			continue
		case ch == '\'' || ch == '"' || ch == '`':
			quote = ch
		}
		word = quote == 0 && strings.IndexByte(" \t\n;&|()", ch) >= 0
	}
	return quote != 0
}

var (
	// NAME = value, NAME =value: sh runs NAME as a command.
	shSpacedEquals = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)[ \t]+=`)
	// NAME+=value is bash's, not sh's: sh runs the word as a command (the
	// chunk J review, J3: what a drop-in that appends would write).
	shAppend = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\+=`)
	// NAME=value with a value sc can judge: plain characters, $NAME and
	// ${NAME} (GRUB_X=$GRUB_X more, the other way a drop-in appends
	// wrongly; J3 too). One with a quote, a backslash, a backquote or any
	// other expansion may be anything, and is not judged.
	shPlainAssign = regexp.MustCompile("^([A-Za-z_][A-Za-z0-9_]*)=((?:[^\"'$`\\\\ \t;&|#]|\\$[A-Za-z_][A-Za-z0-9_]*|\\$\\{[A-Za-z_][A-Za-z0-9_]*\\})*)")
	// A word that is an assignment: sh sets it too.
	shAssignWord = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// shCommandWord returns the name whose value is followed by a word sh runs
// as a command (NAME=value more, NAME= value, NAME=$NAME more), or "".
// Assignments in a row are fine: GRUB_TIMEOUT=0 GRUB_TIMEOUT_STYLE=hidden
// sets both (the chunk J review, J9: that was called a blocker).
func shCommandWord(t string) string {
	for {
		m := shPlainAssign.FindStringSubmatch(t)
		if m == nil {
			return "" // not an assignment sc judges
		}
		rest := t[len(m[0]):]
		next := strings.TrimLeft(rest, " \t")
		switch {
		case len(next) == len(rest), next == "", strings.IndexByte(";&|#", next[0]) >= 0:
			// The value goes on with something sc does not judge (a
			// quote), or the command ends here, or a comment begins.
			return ""
		case shAssignWord.MatchString(next):
			t = next
		default:
			return m[1]
		}
	}
}

// shAssignments finds the mistakes in a shell fragment of NAME=value lines
// that sh -n accepts but update-grub (set -e, then ". FILE") stops at:
// spaces around =, NAME+=, and a value with spaces but no quotes. Lines
// inside a quoted value that runs over several lines are skipped.
func shAssignments(data []byte) []Finding {
	var out []Finding
	quote := byte(0) // the quote open at the end of the line before
	for i, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if quote == 0 {
			t := strings.TrimLeft(l, " \t")
			add := func(text string) {
				out = append(out, Finding{Rule: "grub-default-syntax", Severity: Blocker, Line: i + 1,
					Key: lineKey(data, i+1), Text: text})
			}
			if m := shSpacedEquals.FindStringSubmatch(t); m != nil {
				add(fmt.Sprintf("spaces around = make sh run %s as a command: update-grub stops", m[1]))
			} else if m := shAppend.FindStringSubmatch(t); m != nil {
				add(fmt.Sprintf("%s+= is not sh: sh runs it as a command and update-grub stops (write %s=\"$%s more\")", m[1], m[1], m[1]))
			} else if name := shCommandWord(t); name != "" {
				add(fmt.Sprintf("the value of %s has a space but no quotes: update-grub runs the rest as a command and stops", name))
			}
		}
		quote = shQuoteState(l, quote)
	}
	return out
}

// shQuoteState scans one line as sh would for quotes and returns the
// quote still open at its end (0 for none): backslash escapes outside
// single quotes, and # starting a comment at the start of a word outside
// quotes.
func shQuoteState(l string, quote byte) byte {
	for j := 0; j < len(l); j++ {
		ch := l[j]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			}
		case ch == '\\':
			j++
		case quote == '"':
			if ch == '"' {
				quote = 0
			}
		case ch == '\'' || ch == '"':
			quote = ch
		case ch == '#' && (j == 0 || l[j-1] == ' ' || l[j-1] == '\t'):
			return 0
		}
	}
	return quote
}

var grubSyntaxLine = regexp.MustCompile(`^Syntax error at line (\d+)$`)

// checkGrubCfg checks grub.cfg or custom.cfg with grub-script-check, which
// parses the file as GRUB does at boot. It prints several "error:" lines
// for one mistake and then the line it gave up at: one finding. An unknown
// command is not an error: GRUB looks commands up when it runs them.
func checkGrubCfg(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	res, notes, ok, err := c.validate(ctx, in, "grub-script-check", in.file)
	if err != nil || !ok {
		return nil, notes, err
	}
	var raw, other []string
	line, empty := 0, false
	for _, l := range strings.Split(string(res.Err), "\n") {
		switch {
		case l == "":
		case strings.HasPrefix(l, "error: "):
			raw = append(raw, l)
		case grubSyntaxLine.MatchString(l):
			line, _ = strconv.Atoi(grubSyntaxLine.FindStringSubmatch(l)[1])
			raw = append(raw, l)
		case strings.HasPrefix(l, "Script `") && strings.Contains(l, "contains no commands"):
			empty = true
		default:
			other = append(other, strings.ReplaceAll(l, in.file, in.path))
		}
	}
	switch {
	case len(raw) > 0:
		return []Finding{{Rule: "grubcfg-syntax", Severity: Blocker, Line: line, Key: lineKey(in.data, line),
			Raw: strings.Join(raw, "; "), Text: "GRUB cannot read the menu past this line"}}, notes, nil
	case empty:
		// A grub.cfg with nothing in it leaves GRUB at its prompt;
		// custom.cfg is optional and may well be empty.
		if filepath.Base(in.path) == "grub.cfg" {
			return []Finding{{Rule: "grubcfg-syntax", Severity: Blocker, Text: "the file has no commands, so GRUB has no menu to boot from"}}, notes, nil
		}
	case res.Exit != 0:
		note := fmt.Sprintf("grub-script-check could not check the file (exit %d, no message)", res.Exit)
		if len(other) > 0 {
			note = in.say(fmt.Sprintf("grub-script-check could not check the file (exit %d)", res.Exit), other[0])
		}
		return nil, append(notes, note), nil
	}
	return nil, notes, nil
}

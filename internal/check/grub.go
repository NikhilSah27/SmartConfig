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
		Rule{"grub-default-syntax", Blocker, `update-grub reads /etc/default/grub as a shell script, and this line is
not valid shell. update-grub stops with an error, or writes a grub.cfg
with the wrong settings, and that shows at the next boot.
Check the quotes and brackets on the line: a value with spaces needs
double quotes, and a line is NAME="value".`},
		Rule{"grubcfg-syntax", Blocker, `GRUB cannot read its menu past this line: at the next boot it prints an
error and drops to the grub> prompt instead of booting.
grub.cfg is generated: fix /etc/default/grub or /etc/grub.d and run
update-grub rather than editing it. custom.cfg is yours to fix.`},
	)
}

var shSyntax = regexp.MustCompile(`^(.*): (\d+): Syntax error: (.*)$`)

// checkShSyntax checks /etc/default/grub, a shell fragment that
// update-grub sources, with sh -n (dash on Ubuntu), which stops at the
// first error.
func checkShSyntax(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	res, notes, ok, err := c.validate(ctx, in, "sh", "-n", in.file)
	if err != nil || !ok {
		return nil, notes, err
	}
	var out []Finding
	for _, l := range strings.Split(string(res.Err), "\n") {
		if l == "" {
			continue
		}
		m := shSyntax.FindStringSubmatch(l)
		if m == nil || m[1] != in.file {
			// "sh: 0: cannot open ...", or anything else about itself.
			return out, append(notes, "sh could not check the file ("+strings.ReplaceAll(l, in.file, in.path)+")"), nil
		}
		n, _ := strconv.Atoi(m[2])
		out = append(out, Finding{Rule: "grub-default-syntax", Severity: Blocker, Line: n, Key: lineKey(in.data, n),
			Raw: strings.ReplaceAll(l, in.file, in.path), Text: fmt.Sprintf("the line is not valid shell (%s)", m[3])})
	}
	if res.Exit != 0 && len(out) == 0 {
		return nil, append(notes, fmt.Sprintf("sh could not check the file (exit %d, no message)", res.Exit)), nil
	}
	return out, notes, nil
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
		msg := fmt.Sprintf("exit %d, no message", res.Exit)
		if len(other) > 0 {
			msg = other[0]
		}
		return nil, append(notes, "grub-script-check could not check the file ("+msg+")"), nil
	}
	return nil, notes, nil
}

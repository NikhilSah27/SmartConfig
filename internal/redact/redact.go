// Package redact hides the secret values in a secret file's text (the
// scope's secret lines) and in what a validator says about one: the
// password field of shadow and gshadow, the value of a key=value or
// "key: value" pair whose key names a secret, and the body of a PEM
// private key. Everything else is shown as it is, so a diff still says
// what changed around a secret.
//
// It errs towards hiding: a value under a key that only looks like a
// secret's (802-1x's private-key, a file name) is hidden too.
package redact

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Marker stands in for a hidden value.
const Marker = "<hidden>"

// A Hider returns what stands in for a secret value.
type Hider func(secret string) string

// Plain stands Marker in for every value.
func Plain(string) string { return Marker }

// Numbered returns a Hider that gives each distinct value a number of its
// own, "<hidden 1>", so a diff of two versions still shows which value
// changed. Which hidden values are equal is all it tells.
func Numbered() Hider {
	n := map[string]int{}
	return func(s string) string {
		if n[s] == 0 {
			n[s] = len(n) + 1
		}
		return "<hidden " + strconv.Itoa(n[s]) + ">"
	}
}

var (
	// iniLine is a whole keyfile line, key=value (NetworkManager's
	// system-connections; GKeyFile trims the blanks around "=").
	iniLine = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.@-]+(?:\[[^\]=]*\])?)(\s*=\s*)(.*)$`)
	// iniSection is a keyfile's [section] line.
	iniSection = regexp.MustCompile(`^\s*\[([^\]]*)\]\s*$`)
	// pair is key=value inside a line: a mount option, a validator's
	// words. The value ends at a blank, a comma, a semicolon or a quote.
	pair = regexp.MustCompile(`(^|[\s,;(:'"])([A-Za-z0-9_.-]+)=([^\s,;)'"]*)`)
	// yamlKey is "key:" followed by a blank or the end of the line, after
	// the start, a blank, "{", "," or a list's "- ".
	yamlKey = regexp.MustCompile(`(^|[\s{,])(["']?)([A-Za-z0-9_.-]+)(["']?)\s*:(\s|$)`)
	// pemBegin is the first line of a PEM private key ("-----BEGIN
	// OPENSSH PRIVATE KEY-----", "-----BEGIN ENCRYPTED PRIVATE KEY-----").
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
)

// secretKey reports whether a key's value is a secret: a password, a
// pre-shared key, a PIN or a private key. Flags and types that go with
// one (psk-flags, wep-key-type) are not.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	if i := strings.IndexByte(k, '['); i >= 0 {
		k = k[:i] // a keyfile's localised key, password[de]
	}
	if strings.HasSuffix(k, "-flags") || strings.HasSuffix(k, "-type") {
		return false
	}
	switch k {
	case "pin", "shared", "private": // NM's gsm pin; netplan's WireGuard keys
		return true
	}
	if strings.HasSuffix(k, "-pin") || strings.HasSuffix(k, "_pin") {
		return true
	}
	for _, w := range []string{"password", "passwd", "passphrase", "psk", "secret", "private-key", "private_key", "preshared", "wep-key"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// shadowFile reports whether p is laid out as /etc/shadow is, with the
// password in the second field.
func shadowFile(p string) bool {
	b := path.Base(p)
	return b == "shadow" || b == "gshadow"
}

// File returns data with each secret value replaced by what hide returns
// for it, and the number of values hidden. The shadow rule applies to a
// file named shadow or gshadow, the others to every file.
func File(p string, data []byte, hide Hider) ([]byte, int) {
	n := 0
	h := func(s string) string {
		n++
		return hide(s)
	}
	lines := strings.SplitAfter(string(data), "\n")
	var b strings.Builder
	b.Grow(len(data))
	pem := false     // inside a PEM private key
	cont := -1       // a YAML secret's key column: lines indented past it are its value
	section := false // inside a keyfile section whose every value is a secret
	for _, l := range lines {
		body, end := cut(l)
		switch {
		case pem:
			if strings.Contains(body, "-----END ") {
				pem = false
			} else if strings.TrimSpace(body) != "" {
				body = hideIndented(body, h)
			}
		case cont >= 0 && strings.TrimSpace(body) == "":
			// A blank line inside a block scalar holds nothing.
		case cont >= 0 && indent(body) > cont:
			body = hideIndented(body, h)
		case shadowFile(p):
			cont = -1
			body = shadowLine(body, h)
		default:
			cont = -1
			if m := iniSection.FindStringSubmatch(body); m != nil {
				section = strings.Contains(strings.ToLower(m[1]), "secret") // [vpn-secrets]
				break
			}
			if loc := pemBegin.FindStringIndex(body); loc != nil {
				pem = !strings.Contains(body[loc[1]:], "-----END ")
				break
			}
			if m := iniLine.FindStringSubmatchIndex(body); m != nil && (section || secretKey(body[m[4]:m[5]])) {
				if v := strings.TrimSpace(body[m[8]:m[9]]); v != "" && !hidden(v) {
					body = body[:m[8]] + h(v)
				}
				break
			}
			body = pairs(body, h)
			body, cont = yaml(body, h)
		}
		b.WriteString(body)
		b.WriteString(end)
	}
	return []byte(b.String()), n
}

// Values returns the secret values File hides in data, longest first.
func Values(p string, data []byte) []string {
	seen := map[string]bool{}
	var vs []string
	File(p, data, func(s string) string {
		if !seen[s] {
			seen[s] = true
			vs = append(vs, s)
		}
		return Marker
	})
	sort.SliceStable(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	return vs
}

// Line hides, in s, a line a validator printed about a secret file: each
// of vals (from Values, longest first), then any key=value or "key: value"
// whose key names a secret. A value shorter than 4 bytes is hidden only
// where it stands alone, so "x" does not take every x out of the line.
func Line(s string, vals []string, hide Hider) string {
	for _, v := range vals {
		s = replaceValue(s, v, hide(v))
	}
	s = pairs(s, hide)
	s, _ = yaml(s, hide)
	return s
}

// replaceValue replaces each occurrence of v in s with mark; a short v
// only where no letter or digit touches it.
func replaceValue(s, v, mark string) string {
	if v == "" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, v)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := i + len(v)
		if len(v) >= 4 || (!word(s, i-1) && !word(s, j)) {
			b.WriteString(s[:i])
			b.WriteString(mark)
		} else {
			b.WriteString(s[:j])
		}
		s = s[j:]
	}
}

// word reports whether s[i] is a letter or a digit; false outside s.
func word(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// hidden reports whether v is already a marker.
func hidden(v string) bool { return strings.HasPrefix(v, "<hidden") }

// cut splits a line into its body and its end ("\n", "\r\n" or "").
func cut(l string) (string, string) {
	body := strings.TrimSuffix(l, "\n")
	if len(body) < len(l) && strings.HasSuffix(body, "\r") {
		body = body[:len(body)-1]
	}
	return body, l[len(body):]
}

// indent is the number of leading blanks and tabs of l.
func indent(l string) int {
	return len(l) - len(strings.TrimLeft(l, " \t"))
}

// hideIndented hides all of l but its indentation.
func hideIndented(l string, hide Hider) string {
	i := indent(l)
	v := strings.TrimRight(l[i:], " \t")
	if v == "" || hidden(v) {
		return l
	}
	return l[:i] + hide(v)
}

// shadowLine hides the password field of a shadow or gshadow line. An
// empty field, and one of "!" and "*" alone, holds no secret and says
// whether the account is locked, so it is kept; so is the "!" that locks
// a password. A line with no ":" is hidden whole: it is no account, and
// it may be a password that lost its colons.
func shadowLine(l string, hide Hider) string {
	if strings.TrimSpace(l) == "" {
		return l
	}
	name, rest, ok := strings.Cut(l, ":")
	if !ok {
		return hideIndented(l, hide)
	}
	pw, tail, more := strings.Cut(rest, ":")
	lock := pw[:len(pw)-len(strings.TrimLeft(pw, "!"))]
	if v := pw[len(lock):]; v != "" && strings.Trim(v, "!*") != "" && !hidden(v) {
		pw = lock + hide(v)
	}
	l = name + ":" + pw
	if more {
		l += ":" + tail
	}
	return l
}

// pairs hides the value of each key=value in l whose key names a secret.
func pairs(l string, hide Hider) string {
	ms := pair.FindAllStringSubmatchIndex(l, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if v := l[m[6]:m[7]]; v != "" && !hidden(v) && secretKey(l[m[4]:m[5]]) {
			l = l[:m[6]] + hide(v) + l[m[7]:]
		}
	}
	return l
}

// yaml hides the value of each "key: value" in l whose key names a
// secret. It returns the column of the last such key, or -1: the lines
// indented past it (a block scalar, a folded value, a nested mapping)
// are its value too.
func yaml(l string, hide Hider) (string, int) {
	cont := -1
	for from := 0; from < len(l); {
		m := yamlKey.FindStringSubmatchIndex(l[from:])
		if m == nil {
			break
		}
		for k := range m {
			if m[k] >= 0 {
				m[k] += from
			}
		}
		from = m[1]
		if !secretKey(l[m[6]:m[7]]) {
			continue
		}
		cont = m[4]
		v, end := yamlValue(l, m[1], strings.ContainsAny(l[:m[4]], "{["))
		if v != "" && !hidden(v) && v != "|" && v != ">" && !strings.HasPrefix(v, "|") && !strings.HasPrefix(v, ">") {
			mark := hide(v)
			l = l[:end-len(v)] + mark + l[end:]
			from = end - len(v) + len(mark)
		}
	}
	return l, cont
}

// yamlValue returns the scalar that starts at or after l[i] and the index
// just past it: a quoted string to its closing quote, else plain text to
// a comment (" #"), or, in a flow collection, to the next "," "}" or "]".
func yamlValue(l string, i int, flow bool) (string, int) {
	for i < len(l) && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	if i == len(l) {
		return "", i
	}
	if q := l[i]; q == '"' || q == '\'' {
		j := i + 1
		for j < len(l) {
			switch {
			case q == '"' && l[j] == '\\':
				j++
			case l[j] == q && q == '\'' && j+1 < len(l) && l[j+1] == '\'':
				j++ // '' is a quote inside a single-quoted string
			case l[j] == q:
				return l[i : j+1], j + 1
			}
			j++
		}
		return l[i:], len(l) // no closing quote: the rest of the line
	}
	j := i
	for j < len(l) {
		if l[j] == '#' && (l[j-1] == ' ' || l[j-1] == '\t') {
			break
		}
		if flow && (l[j] == ',' || l[j] == '}' || l[j] == ']') {
			break
		}
		j++
	}
	v := strings.TrimRight(l[i:j], " \t")
	return v, i + len(v)
}

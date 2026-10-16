// Package redact hides the secret values in a secret file's text (the
// scope's secret lines) and in what a validator says about one: the
// password field of shadow and gshadow and any crypt hash in that place,
// the value of a key=value, "key: value" or "key value" pair whose key
// names a secret, the body of a PEM or PGP private key, and OpenVPN's
// inline keys. Everything else is shown as it is, so a diff still says
// what changed around a secret. A file that is only a secret (the scope's
// hide lines) is hidden whole, by Whole.
//
// It errs towards hiding: a value under a key that only looks like a
// secret's (802-1x's private-key, a file name) is hidden too, and so is a
// mapping under a secret key.
package redact

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Marker stands in for a hidden value.
const Marker = "<hidden>"

// A Hider returns what stands in for a secret value.
type Hider func(secret string) string

// Plain stands Marker in for every value.
func Plain(string) string { return Marker }

// The markers of a diff for a value only one of its two versions hides:
// Old in the first version, New in the second.
const (
	Old = "<hidden, old>"
	New = "<hidden, new>"
)

// Sides returns the Hiders of a diff's two versions, given the values
// each one hides (a, the old, then b, the new): Marker for a value the other version
// hides too, anywhere in it, else Old or New. So a changed value shows as
// changed, and nothing tells which hidden values are equal: two accounts
// with one hash look the same as two with different ones.
func Sides(a, b []string) (Hider, Hider) {
	in := func(vs []string) map[string]bool {
		m := make(map[string]bool, len(vs))
		for _, v := range vs {
			m[v] = true
		}
		return m
	}
	inA, inB := in(a), in(b)
	side := func(other map[string]bool, alone string) Hider {
		return func(s string) string {
			if other[s] {
				return Marker
			}
			return alone
		}
	}
	return side(inB, Old), side(inA, New)
}

var (
	// iniLine is a whole keyfile line, key=value (NetworkManager's
	// system-connections; GKeyFile trims the blanks around "=").
	iniLine = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.@-]+(?:\[[^\]=]*\])?)(\s*=\s*)(.*)$`)
	// iniAny is a keyfile line in a section whose every value is a secret.
	// GKeyFile allows any key but "=" and brackets, blanks included
	// (vpnc's "IPSec secret").
	iniAny = regexp.MustCompile(`^(\s*)([^=\s\[\]][^=\[\]]*?(?:\[[^\]=]*\])?)(\s*=\s*)(.*)$`)
	// iniSection is a keyfile's [section] line. A name with "{", "," or
	// ": " in it is a YAML flow sequence on a line of its own, which the
	// YAML rule reads instead.
	iniSection = regexp.MustCompile(`^\s*\[([^\]{,]*)\]\s*$`)
	// pair is key=value inside a line: a mount option, a validator's
	// words. The value ends at a blank, a comma, a semicolon or a quote.
	pair = regexp.MustCompile(`(^|[\s,;(:'"])([A-Za-z0-9_.-]+)=([^\s,;)'"]*)`)
	// yamlKey is "key:" after the start, a blank, "{", "[", "," or a
	// list's "- ", followed by a blank or the end of the line, or, after a quoted
	// key, straight by a quoted value ({"password":"x"}, JSON's way).
	yamlKey = regexp.MustCompile(`(^|[\s{\[,])(["']?)([A-Za-z0-9_.-]+)(["']?)\s*:(\s|$|["'])`)
	// yamlExplicit is an explicit key, "? key", whose value follows on a
	// line of its own, ": value".
	yamlExplicit = regexp.MustCompile(`^\s*\?\s+["']?([A-Za-z0-9_.-]+)["']?\s*$`)
	// spaceLine is "key value", the way msmtp, nslcd and ifupdown write
	// theirs (password x, bindpw x, wpa-psk x).
	spaceLine = regexp.MustCompile(`^(\s*)([A-Za-z][A-Za-z0-9_.-]*)([ \t]+)(\S.*)$`)
	// cryptField is a line laid out as shadow's, a name then a crypt hash
	// ($id$...), with any "!" that locks it: a copy of shadow by any name.
	cryptField = regexp.MustCompile(`^[^:\s#]+:!*(\$[0-9a-z]+\$[^:\s]*)`)
	// ovpnBlock is the first line of an OpenVPN inline key or secret,
	// which runs to its closing tag.
	ovpnBlock = regexp.MustCompile(`^\s*<(key|secret|tls-auth|tls-crypt|tls-crypt-v2|auth-user-pass|http-proxy-user-pass|pkcs12)>\s*$`)
	// pemBegin is the first line of a PEM private key, of any kind
	// (OPENSSH, ENCRYPTED, RSA ...), or of a PGP one ("PRIVATE KEY
	// BLOCK").
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
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
	// A PIN after any separator: gsm-pin, sim_pin, and netplan's
	// passthrough of NM's settings, 802-1x.pin. Then ssmtp's AuthPass,
	// nslcd's bindpw and cloud-init's rsa_private.
	for _, s := range []string{"-pin", "_pin", ".pin", "pass", "pw", "_private", "-private"} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	for _, w := range []string{"password", "passwd", "passphrase", "psk", "secret", "private-key", "private_key", "preshared", "wep-key", "mka-cak", "authtok", "token"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// shadowFile reports whether p is laid out as /etc/shadow is, with the
// password in the second field: shadow, gshadow and any copy of them
// named after them (shadow.backup, old-gshadow).
func shadowFile(p string) bool {
	return strings.Contains(path.Base(p), "shadow")
}

// A span is the bytes [from, to) of a line that hold a secret value.
type span struct{ from, to int }

// apply replaces each span of l with what hide returns for its text.
// Spans that overlap or touch are one value: each rule finds its spans in
// the line as it is, so no rule reads another's marker.
func apply(l string, sp []span, hide Hider) string {
	if len(sp) == 0 {
		return l
	}
	sort.Slice(sp, func(i, j int) bool { return sp[i].from < sp[j].from })
	var b strings.Builder
	at := 0
	for i := 0; i < len(sp); {
		s := sp[i]
		for i++; i < len(sp) && sp[i].from <= s.to; i++ {
			s.to = max(s.to, sp[i].to)
		}
		b.WriteString(l[at:s.from])
		b.WriteString(hide(l[s.from:s.to]))
		at = s.to
	}
	b.WriteString(l[at:])
	return b.String()
}

// File returns data with each secret value replaced by what hide returns
// for it, and the number of values hidden. The shadow rule applies to a
// file named after shadow or gshadow, the others to every file.
func File(p string, data []byte, hide Hider) ([]byte, int) {
	n := 0
	h := func(s string) string {
		n++
		return hide(s)
	}
	lines := strings.SplitAfter(string(data), "\n")
	var b strings.Builder
	b.Grow(len(data))
	st := yamlState{cont: -1}
	pem := false     // inside a PEM private key
	section := false // inside a keyfile section whose every value is a secret
	tag := ""        // inside an OpenVPN inline key: its closing tag
	for _, l := range lines {
		body, end := cut(l)
		switch {
		case pem:
			if strings.Contains(body, "-----END ") {
				pem = false
			} else {
				body = hideIndented(body, h)
			}
		case tag != "":
			if strings.TrimSpace(body) == tag {
				tag = ""
			} else {
				body = hideIndented(body, h)
			}
		case st.quote != 0:
			body = apply(body, st.closeQuote(body), h)
		case st.flowNext && strings.TrimSpace(body) != "":
			st.flowNext = false
			body = apply(body, st.value(body, 0, true, true), h)
		case st.explicit && strings.HasPrefix(strings.TrimSpace(body), ":"):
			st.explicit = false
			body = apply(body, st.value(body, strings.IndexByte(body, ':')+1, false, true), h)
		case st.cont >= 0 && strings.TrimSpace(body) == "":
			// A blank line inside a block scalar holds nothing.
		case st.cont >= 0 && indent(body) > st.cont:
			body = hideIndented(body, h)
		case shadowFile(p):
			st = yamlState{cont: -1}
			body = shadowLine(body, h)
		default:
			st = yamlState{cont: -1}
			if m := iniSection.FindStringSubmatch(body); m != nil && !strings.Contains(m[1], ": ") {
				section = strings.Contains(strings.ToLower(m[1]), "secret") // [vpn-secrets]
				break
			}
			if m := ovpnBlock.FindStringSubmatch(body); m != nil {
				tag = "</" + m[1] + ">"
				break
			}
			var sp []span
			if m := cryptField.FindStringSubmatchIndex(body); m != nil {
				sp = append(sp, span{m[2], m[3]})
			}
			if loc := pemBegin.FindStringIndex(body); loc != nil {
				if e := strings.Index(body[loc[1]:], "-----END "); e < 0 {
					pem = true
					if v := strings.TrimRight(body[loc[1]:], " \t"); strings.TrimSpace(v) != "" {
						sp = append(sp, span{loc[1], loc[1] + len(v)}) // the body begins on BEGIN's line
					}
				} else if strings.TrimSpace(body[loc[1]:loc[1]+e]) != "" {
					sp = append(sp, span{loc[1], loc[1] + e}) // a key on one line
				}
			}
			re := iniLine
			if section {
				re = iniAny
			}
			if m := re.FindStringSubmatchIndex(body); m != nil && (section || secretKey(body[m[4]:m[5]])) {
				if v := strings.TrimRight(body[m[8]:m[9]], " \t"); v != "" {
					sp = append(sp, span{m[8], m[8] + len(v)})
				}
			} else if m := spaceLine.FindStringSubmatchIndex(body); m != nil && secretKey(body[m[4]:m[5]]) {
				sp = append(sp, span{m[8], m[8] + len(strings.TrimRight(body[m[8]:m[9]], " \t"))})
			} else {
				sp = append(sp, pairs(body)...)
				sp = append(sp, st.yaml(body)...)
				if m := yamlExplicit.FindStringSubmatch(body); m != nil && secretKey(m[1]) {
					st.explicit, st.cont = true, indent(body)
				}
			}
			body = apply(body, sp, h)
		}
		b.WriteString(body)
		b.WriteString(end)
	}
	return []byte(b.String()), n
}

// Whole returns data with the text of every line, but its indentation,
// replaced by what hide returns for it, and the number of lines hidden: a
// file that is only a secret.
func Whole(data []byte, hide Hider) ([]byte, int) {
	n := 0
	var b strings.Builder
	b.Grow(len(data))
	for _, l := range strings.SplitAfter(string(data), "\n") {
		body, end := cut(l)
		if strings.TrimSpace(body) != "" {
			n++
			body = hideIndented(body, hide)
		}
		b.WriteString(body)
		b.WriteString(end)
	}
	return []byte(b.String()), n
}

// Values returns the secret values File (or, with whole, Whole) hides in
// data, longest first, and the other forms a validator may quote them in:
// without their quotes, and with YAML's or a keyfile's escapes undone.
func Values(p string, data []byte, whole bool) []string {
	seen := map[string]bool{}
	var vs []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			vs = append(vs, s)
		}
	}
	collect := func(s string) string {
		add(s)
		for _, u := range unquoted(s) {
			add(u)
			add(strings.TrimSpace(u))
		}
		return Marker
	}
	if whole {
		Whole(data, collect)
	} else {
		File(p, data, collect)
	}
	sort.SliceStable(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	return vs
}

// unquoted returns the forms of s a parser makes of it: the text inside
// its quotes, with a doubled single quote or the backslash escapes undone,
// and s with a keyfile's escapes undone.
func unquoted(s string) []string {
	var us []string
	if t := strings.TrimSpace(s); len(t) >= 2 && (t[0] == '"' || t[0] == '\'') && t[len(t)-1] == t[0] {
		in := t[1 : len(t)-1]
		us = append(us, in)
		if t[0] == '\'' {
			us = append(us, strings.ReplaceAll(in, "''", "'"))
		} else {
			us = append(us, unescape.Replace(in))
		}
	}
	if strings.Contains(s, `\`) {
		us = append(us, unescape.Replace(s))
	}
	return us
}

// unescape undoes the backslash escapes YAML's double quotes and GKeyFile
// share: \s (GKeyFile's blank), \t, \n, \r, \" and \\.
var unescape = strings.NewReplacer(`\\`, `\`, `\s`, " ", `\t`, "\t", `\n`, "\n", `\r`, "\r", `\"`, `"`)

// Line hides, in s, a line a validator printed about a secret file: each
// of vals (from Values; see valueSpans), then any key=value or
// "key: value" whose key names a secret.
func Line(s string, vals []string, hide Hider) string {
	var sp []span
	for _, v := range vals {
		sp = append(sp, valueSpans(s, v)...)
	}
	sp = append(sp, pairs(s)...)
	st := yamlState{cont: -1}
	sp = append(sp, st.yaml(s)...)
	return apply(s, sp, hide)
}

// valueSpans returns where v stands in s. A value shorter than 8 bytes
// counts only where it stands alone, with no letter or digit right before
// or after it: inside a longer word (a PIN inside a file name the line
// also prints in full) where a marker landed would tell it.
func valueSpans(s, v string) []span {
	var sp []span
	for from := 0; v != "" && from < len(s); {
		i := strings.Index(s[from:], v)
		if i < 0 {
			break
		}
		i += from
		j := i + len(v)
		if len(v) >= 8 || !word(s, i-1) && !word(s, j) {
			sp = append(sp, span{i, j})
		}
		from = i + 1
	}
	return sp
}

// word reports whether s[i] is a letter or a digit; false outside s.
func word(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

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
	if v == "" {
		return l
	}
	return l[:i] + hide(v) + l[i+len(v):]
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
	if v := pw[len(lock):]; v != "" && strings.Trim(v, "!*") != "" {
		pw = lock + hide(v)
	}
	l = name + ":" + pw
	if more {
		l += ":" + tail
	}
	return l
}

// pairs returns the spans of the values of each key=value in l whose key
// names a secret.
func pairs(l string) []span {
	var sp []span
	for _, m := range pair.FindAllStringSubmatchIndex(l, -1) {
		if m[6] < m[7] && secretKey(l[m[4]:m[5]]) {
			sp = append(sp, span{m[6], m[7]})
		}
	}
	return sp
}

// yamlState is what a secret YAML value left open at the end of a line
// asks of the lines after it.
type yamlState struct {
	cont     int  // the secret key's column: lines indented past it are its value; -1 for none
	quote    byte // a quoted secret value not closed yet: its quote
	flowNext bool // a secret key in a flow collection, its value still to come
	explicit bool // "? key" named a secret: the ": value" line after it is its value
}

// yaml returns the spans of the values of each "key: value" in l whose
// key names a secret, and notes in st what such a value leaves open for
// the lines after it: lines indented past the key (a block scalar, a
// folded value, a nested mapping), a quote not closed yet, a flow
// mapping's value still to come. A key in a comment has its value hidden
// but leaves nothing open.
func (st *yamlState) yaml(l string) []span {
	var sp []span
	com := commentAt(l)
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
		start := m[1]
		if m[10] < m[11] && (l[m[10]] == '"' || l[m[10]] == '\'') {
			if m[8] == m[9] {
				continue // a value straight after ":" needs a quoted key
			}
			start = m[10]
		}
		if !secretKey(l[m[6]:m[7]]) {
			continue
		}
		open := m[4] < com
		if open {
			st.cont = m[4]
		}
		flow := strings.ContainsAny(l[:m[4]], "{[")
		v := st.value(l, start, flow, open)
		if v == nil && flow && open {
			st.flowNext = true
		}
		for _, s := range v {
			from = max(from, s.to)
		}
		sp = append(sp, v...)
	}
	return sp
}

// value returns the span of the scalar that starts at or after l[i] (see
// yamlValue): none for an empty value or a block scalar's "|" or ">".
// With open, a quote the line leaves unclosed is noted in st.
func (st *yamlState) value(l string, i int, flow, open bool) []span {
	from, to, unclosed := yamlValue(l, i, flow)
	if from == to || l[from] == '|' || l[from] == '>' {
		return nil
	}
	if unclosed && open {
		st.quote = l[from]
	}
	return []span{{from, to}}
}

// closeQuote returns the span of l still inside the quoted value an
// earlier line left open: up to its closing quote, or all of l.
func (st *yamlState) closeQuote(l string) []span {
	i := indent(l)
	j := closing(l, i, st.quote)
	if j < 0 {
		j = len(strings.TrimRight(l, " \t"))
	} else {
		st.quote = 0
	}
	if j <= i {
		return nil
	}
	return []span{{i, j}}
}

// yamlValue returns where the scalar that starts at or after l[i] begins
// and ends, and whether it is a quoted string the line leaves unclosed: a
// quoted string runs to its closing quote, else plain text to a comment
// (" #"), or, in a flow collection, to the next "," "}" or "]".
func yamlValue(l string, i int, flow bool) (int, int, bool) {
	for i < len(l) && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	if i == len(l) {
		return i, i, false
	}
	if q := l[i]; q == '"' || q == '\'' {
		if j := closing(l, i+1, q); j >= 0 {
			return i, j + 1, false
		}
		return i, len(l), true
	}
	j := i
	for j < len(l) {
		if l[j] == '#' && (j == i || l[j-1] == ' ' || l[j-1] == '\t') {
			break
		}
		if flow && (l[j] == ',' || l[j] == '}' || l[j] == ']') {
			break
		}
		j++
	}
	return i, i + len(strings.TrimRight(l[i:j], " \t")), false
}

// closing returns the index of the quote q that closes a string whose
// text starts at l[i], or -1: a backslash escapes in double quotes, and
// a doubled quote is one quote in single quotes.
func closing(l string, i int, q byte) int {
	for j := i; j < len(l); j++ {
		switch {
		case q == '"' && l[j] == '\\':
			j++
		case q == '\'' && l[j] == q && j+1 < len(l) && l[j+1] == q:
			j++
		case l[j] == q:
			return j
		}
	}
	return -1
}

// commentAt returns where the comment of a YAML line starts: a "#" at its
// start or after a blank, outside quotes; len(l) if it has none.
func commentAt(l string) int {
	var q byte
	for j := 0; j < len(l); j++ {
		c := l[j]
		switch {
		case q == '"' && c == '\\':
			j++
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#' && (j == 0 || l[j-1] == ' ' || l[j-1] == '\t'):
			return j
		}
	}
	return len(l)
}

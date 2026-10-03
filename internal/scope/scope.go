package scope

import (
	_ "embed"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
)

//go:embed default.scope
var defaultText string

// Scope is a parsed scope file (plan 7.1-7.3): roots, include/exclude rules,
// fingerprint-only (digest) rules and tiers.
type Scope struct {
	roots  []string
	rules  []rule // include, exclude, digest and tier lines, in file order
	scHome string // sc's own data directory: never recorded
}

type rule struct {
	verb string // include, exclude, digest or tier
	tier int
	g    *glob
	line int
}

var (
	defaultOnce  sync.Once
	defaultScope *Scope
)

// Default returns the embedded default scope, parsed once. Its text is
// fixed at build time and tested, so a parse error is a bug in sc.
func Default() *Scope {
	defaultOnce.Do(func() {
		s, err := Parse(defaultText)
		if err != nil {
			panic("scope: default.scope: " + err.Error())
		}
		defaultScope = s
	})
	return defaultScope
}

// Parse reads a scope file. Lines are "root DIR", "include GLOB",
// "exclude GLOB", "digest GLOB" or "tier N GLOB"; blank lines and lines
// starting with "#" are skipped. Every pattern is validated.
func Parse(text string) (*Scope, error) {
	s := &Scope{}
	for i, l := range strings.Split(text, "\n") {
		if err := s.parseLine(strings.TrimSpace(l), i+1); err != nil {
			return nil, fmt.Errorf("scope line %d: %w", i+1, err)
		}
	}
	if len(s.roots) == 0 {
		return nil, errors.New("scope has no root")
	}
	return s, nil
}

func (s *Scope) parseLine(l string, n int) error {
	if l == "" || strings.HasPrefix(l, "#") {
		return nil
	}
	f := strings.Fields(l)
	want := 2
	if f[0] == "tier" {
		want = 3
	}
	switch f[0] {
	case "root", "include", "exclude", "digest", "tier":
	default:
		return fmt.Errorf("unknown keyword %q", f[0])
	}
	if len(f) != want {
		return fmt.Errorf("%s takes %d field(s), got %d", f[0], want-1, len(f)-1)
	}
	if f[0] == "root" {
		if !cleanAbs(f[1]) || f[1] == "/" {
			return fmt.Errorf("root %q is not a clean absolute directory", f[1])
		}
		s.roots = append(s.roots, f[1])
		return nil
	}
	r := rule{verb: f[0], line: n}
	pattern := f[1]
	if f[0] == "tier" {
		t, err := strconv.Atoi(f[1])
		if err != nil || t < 1 || t > 4 {
			return fmt.Errorf("tier %q is not 1 to 4", f[1])
		}
		r.tier, pattern = t, f[2]
	}
	g, err := compileGlob(pattern)
	if err != nil {
		return err
	}
	r.g = g
	s.rules = append(s.rules, r)
	return nil
}

// cleanAbs reports whether p is an absolute path in clean form.
func cleanAbs(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p
}

// With returns a copy of s for one machine: each of sshRoots (the usable
// login .ssh directories) becomes a root, and nothing under scHome, sc's
// own data directory, is ever recorded. Both are cleaned ("/var/sc/"
// excludes /var/sc); relative paths and "/" are ignored, so the caller
// passes absolute ones.
func (s *Scope) With(scHome string, sshRoots []string) *Scope {
	c := &Scope{rules: s.rules, scHome: s.scHome}
	c.roots = append(c.roots, s.roots...)
	for _, r := range sshRoots {
		if r, ok := clean(r); ok && r != "/" && !contains(c.roots, r) {
			c.roots = append(c.roots, r)
		}
	}
	if h, ok := clean(scHome); ok && h != "/" {
		c.scHome = h
	}
	return c
}

// clean returns the clean form of an absolute path, and false for a
// relative one.
func clean(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	return path.Clean(p), true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Roots returns the directories under which paths are recorded.
func (s *Scope) Roots() []string { return append([]string(nil), s.roots...) }

// Recorded reports whether scd records p (plan 7.2): p lies under a root,
// and neither p nor any directory between that root and p is excluded. A
// root itself is never excluded. p must be a clean absolute path.
func (s *Scope) Recorded(p string) bool {
	return s.decide(p).recorded
}

// decision is what decide finds about a path.
type decision struct {
	recorded bool
	own      bool   // under sc's own data directory
	root     string // the root it lies under; "" when under none
	line     int    // the include or exclude line that decided, 0 when none
	at       string // the path that line matched: p, or a directory above it
}

// decide says whether p is recorded and which line decides it. Recorded
// (scd) and Explain (sc scope) both use it, so they cannot disagree.
func (s *Scope) decide(p string) decision {
	var d decision
	if !cleanAbs(p) {
		return d
	}
	if s.scHome != "" && under(p, s.scHome) {
		d.own = true
		return d
	}
	for _, r := range s.roots {
		if under(p, r) && len(r) > len(d.root) {
			d.root = r
		}
	}
	if d.root == "" {
		return d
	}
	// Walk down from the root, so the first excluded directory ends the
	// walk: a deep path under it costs one check, not one per level.
	for i := len(d.root) + 1; i <= len(p); i++ {
		if i < len(p) && p[i] != '/' {
			continue
		}
		ok, line := s.self(p[:i])
		if !ok {
			d.line, d.at = line, p[:i]
			return d
		}
		if i == len(p) && line != 0 {
			d.line, d.at = line, p // an include line keeps p itself
		}
	}
	d.recorded = true
	return d
}

// under reports whether p is dir or lies below it.
func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// self applies the include and exclude rules to p alone: the first matching
// line wins, and no match means included.
func (s *Scope) self(p string) (bool, int) {
	for _, r := range s.rules {
		if (r.verb == "include" || r.verb == "exclude") && r.g.match(p) {
			return r.verb == "include", r.line
		}
	}
	return true, 0
}

// Line is one line of the scope file: its number and its text.
type Line struct {
	N    int
	Text string
}

// Why is what the scope says about one path, with the line that decides
// each answer (a zero Line when none does).
type Why struct {
	Path     string
	Recorded bool
	Root     string // the root p lies under; "" when it lies under none
	Own      bool   // p lies under sc's own data directory
	Rule     Line   // the include or exclude line that decided
	At       string // the path Rule matched: p, or a directory above it
	Tier     int
	TierRule Line
	Digest   bool // fingerprint-only
	DigRule  Line
}

// Explain says why p is or is not recorded, its tier and whether it is
// fingerprint-only (sc scope). p is cleaned first; a relative p is outside
// every root.
func (s *Scope) Explain(p string) Why {
	w := Why{Path: p, Tier: 4}
	p, ok := clean(p)
	if !ok {
		return w
	}
	w.Path = p
	if r := s.first("tier", p); r != nil {
		w.Tier, w.TierRule = r.tier, s.line(*r)
	}
	if r := s.first("digest", p); r != nil {
		w.Digest, w.DigRule = true, s.line(*r)
	}
	d := s.decide(p)
	w.Recorded, w.Own, w.Root = d.recorded, d.own, d.root
	if d.line != 0 {
		w.Rule, w.At = s.lineAt(d.line), d.at
	}
	return w
}

// first returns the first line of verb ("tier" or "digest") whose pattern
// matches p, or nil.
func (s *Scope) first(verb, p string) *rule {
	for i := range s.rules {
		if s.rules[i].verb == verb && s.rules[i].g.match(p) {
			return &s.rules[i]
		}
	}
	return nil
}

func (s *Scope) line(r rule) Line {
	text := r.verb + " " + r.g.text
	if r.verb == "tier" {
		text = fmt.Sprintf("tier %d %s", r.tier, r.g.text)
	}
	return Line{N: r.line, Text: text}
}

func (s *Scope) lineAt(n int) Line {
	for _, r := range s.rules {
		if r.line == n {
			return s.line(r)
		}
	}
	return Line{}
}

// Tier returns how loudly a change of p is logged: the first matching tier
// line, else 4 (1 boot, 2 access, 3 network). p is cleaned first.
func (s *Scope) Tier(p string) int {
	p, _ = clean(p)
	if r := s.first("tier", p); r != nil {
		return r.tier
	}
	return 4
}

// FingerprintOnly reports whether only p's sha256, mode and owner may be
// stored: its content is never stored, shown or restored. It holds for any
// matching digest line, whether or not p is recorded, so every writer
// (scd, sc snapshot, a restore's pre-restore row) obeys it.
//
// p is cleaned first, so "/etc/ssh//ssh_host_rsa_key" is caught too. A
// relative p gets true: a caller that passes one has a bug, and a
// fingerprint is the answer that cannot leak a secret.
func (s *Scope) FingerprintOnly(p string) bool {
	p, ok := clean(p)
	if !ok {
		return true
	}
	return s.first("digest", p) != nil
}

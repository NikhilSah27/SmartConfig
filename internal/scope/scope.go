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
	ok, _ := s.decide(p)
	return ok
}

// decide is Recorded, also returning the line of the exclude rule that
// drops p (0 when p is recorded, -1 when it lies outside every root or
// under sc's data directory).
func (s *Scope) decide(p string) (bool, int) {
	if !cleanAbs(p) {
		return false, -1
	}
	if s.scHome != "" && under(p, s.scHome) {
		return false, -1
	}
	root := ""
	for _, r := range s.roots {
		if under(p, r) && len(r) > len(root) {
			root = r
		}
	}
	if root == "" {
		return false, -1
	}
	// Walk down from the root, so the first excluded directory ends the
	// walk: a deep path under it costs one check, not one per level.
	for i := len(root) + 1; i <= len(p); i++ {
		if i < len(p) && p[i] != '/' {
			continue
		}
		if ok, line := s.self(p[:i]); !ok {
			return false, line
		}
	}
	return true, 0
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

// Tier returns how loudly a change of p is logged: the first matching tier
// line, else 4 (1 boot, 2 access, 3 network). p is cleaned first.
func (s *Scope) Tier(p string) int {
	p, _ = clean(p)
	for _, r := range s.rules {
		if r.verb == "tier" && r.g.match(p) {
			return r.tier
		}
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
	for _, r := range s.rules {
		if r.verb == "digest" && r.g.match(p) {
			return true
		}
	}
	return false
}

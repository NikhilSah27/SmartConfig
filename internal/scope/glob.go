// Package scope decides which paths scd records. It is pure: no inotify, no
// store, no disk access.
package scope

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// glob is a compiled pattern of the scope file's glob language (plan 7.2):
//
//   - a pattern is an absolute path, or starts with "**/", split on "/";
//   - "**" as a whole segment matches zero or more segments, so "/a/**"
//     matches "/a" itself;
//   - "*" matches any run of characters within a segment, a leading dot
//     included; "?" matches one character;
//   - "[...]" is a class in path.Match syntax: "[^x]" negates, "[!x]" is the
//     class of "!" and "x", and a literal "-" must be escaped ("[.\-]");
//   - "\" escapes the next character;
//   - "{a,b,}" gives alternatives, expanded as text before the split, so an
//     alternative may hold "/" or "*". Several groups are allowed, nesting
//     is not, and an alternative may be empty.
type glob struct {
	text string
	alts [][]string // segments after the leading "/", one list per alternative
}

// compileGlob parses and validates pattern. Every segment is checked with
// path.Match, so a bad pattern is an error here and never at match time.
func compileGlob(pattern string) (*glob, error) {
	g, err := compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", pattern, err)
	}
	return g, nil
}

func compile(pattern string) (*glob, error) {
	if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") {
		return nil, errors.New("must start with / or **/")
	}
	texts, err := expand(pattern)
	if err != nil {
		return nil, err
	}
	g := &glob{text: pattern}
	for _, t := range texts {
		segs := strings.Split(strings.TrimPrefix(t, "/"), "/")
		for _, s := range segs {
			switch {
			case s == "":
				return nil, errors.New("empty segment (a doubled or trailing /)")
			case s == "." || s == "..":
				return nil, fmt.Errorf("segment %q", s)
			case s == "**":
			case bareDoubleStar(s):
				return nil, fmt.Errorf("** must be a whole segment, not %q", s)
			default:
				// path.Match checks the whole pattern even when the name
				// does not match.
				if _, err := path.Match(s, ""); err != nil {
					return nil, fmt.Errorf("segment %q: %w", s, err)
				}
			}
		}
		g.alts = append(g.alts, segs)
	}
	return g, nil
}

// bareDoubleStar reports whether segment s holds two adjacent stars outside
// a class, neither of them escaped. As written in a scope file: a** and
// a\\** (an escaped backslash, then **) do; \** (an escaped star, then a
// star), [**] and *a* do not.
func bareDoubleStar(s string) bool {
	inClass, star := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			i++
			star = false
		case inClass:
			inClass = c != ']'
		case c == '[':
			inClass, star = true, false
		case c == '*':
			if star {
				return true
			}
			star = true
		default:
			star = false
		}
	}
	return false
}

// expand returns every text the brace groups of p give, in order. Escaped
// characters and "[...]" classes are copied as they are: "\{", "\," and a
// "," or "{" inside a class do not count.
func expand(p string) ([]string, error) {
	open, close := -1, -1
	var commas []int
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\':
			i++ // path.Match rejects a trailing "\" later
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
		case c == '{':
			if open >= 0 {
				return nil, errors.New("nested braces")
			}
			open = i
		case c == ',' && open >= 0:
			commas = append(commas, i)
		case c == '}':
			if open < 0 {
				return nil, errors.New("} without {")
			}
			close = i
		}
		if close >= 0 {
			break
		}
	}
	if open < 0 {
		return []string{p}, nil
	}
	if close < 0 {
		return nil, errors.New("{ without }")
	}
	rest, err := expand(p[close+1:])
	if err != nil {
		return nil, err
	}
	var out []string
	start := open + 1
	for _, end := range append(commas, close) {
		for _, r := range rest {
			out = append(out, p[:open]+p[start:end]+r)
		}
		start = end + 1
	}
	return out, nil
}

// match reports whether p matches the pattern. p must be an absolute, clean
// path; it is taken as raw bytes.
func (g *glob) match(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	var name []string
	if p != "/" {
		name = strings.Split(p[1:], "/")
	}
	for _, a := range g.alts {
		if matchSegs(a, name) {
			return true
		}
	}
	return false
}

func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true
			}
			for i := range name {
				if matchSegs(pat, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		// The pattern was validated, so Match cannot fail.
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

func (g *glob) String() string { return g.text }

// Glob is a compiled pattern of the same glob language, for sc's other
// built-in tables (the M3 file graph).
type Glob struct{ g *glob }

// CompileGlob parses and validates pattern.
func CompileGlob(pattern string) (*Glob, error) {
	g, err := compileGlob(pattern)
	if err != nil {
		return nil, err
	}
	return &Glob{g}, nil
}

// Match reports whether p, an absolute clean path, matches the pattern.
func (g *Glob) Match(p string) bool { return g.g.match(p) }

func (g *Glob) String() string { return g.g.String() }

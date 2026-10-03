package check

import (
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"smartconfig/internal/scope"
)

//go:embed default.graph
var defaultGraphText string

// Graph is the parsed file graph (plan 4): which checker reads a file,
// when a saved change takes effect, and the mode of a file sc edit creates.
type Graph struct {
	checks  []graphRule
	modes   []graphRule
	applies map[string]string // checker name -> text
}

type graphRule struct {
	g       *scope.Glob
	checker string      // check lines
	mode    os.FileMode // mode lines
}

var (
	defaultGraphOnce sync.Once
	defaultGraph     *Graph
)

// DefaultGraph returns the embedded graph, parsed once. Its text is checked
// by the tests, so a parse error here is a build mistake.
func DefaultGraph() *Graph {
	defaultGraphOnce.Do(func() {
		g, err := ParseGraph(defaultGraphText)
		if err != nil {
			panic("default.graph: " + err.Error())
		}
		defaultGraph = g
	})
	return defaultGraph
}

var checkerName = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// ParseGraph reads a graph file. Lines are "check NAME GLOB...", "apply
// NAME TEXT" or "mode GLOB OCTAL"; blank lines and lines starting with "#"
// are skipped.
func ParseGraph(text string) (*Graph, error) {
	g := &Graph{applies: map[string]string{}}
	known := map[string]bool{}
	applyLine := map[string]int{}
	for i, l := range strings.Split(text, "\n") {
		if err := g.parseLine(strings.TrimSpace(l), known, applyLine, i+1); err != nil {
			return nil, fmt.Errorf("graph line %d: %w", i+1, err)
		}
	}
	for name, n := range applyLine {
		if !known[name] {
			return nil, fmt.Errorf("graph line %d: apply %s: no check line names %s", n, name, name)
		}
	}
	return g, nil
}

func (g *Graph) parseLine(l string, known map[string]bool, applyLine map[string]int, n int) error {
	if l == "" || strings.HasPrefix(l, "#") {
		return nil
	}
	f := strings.Fields(l)
	switch f[0] {
	case "check":
		if len(f) < 3 {
			return fmt.Errorf("check takes a name and at least one glob")
		}
		if !checkerName.MatchString(f[1]) {
			return fmt.Errorf("checker name %q is not lower-case letters and digits", f[1])
		}
		for _, p := range f[2:] {
			gl, err := scope.CompileGlob(p)
			if err != nil {
				return err
			}
			g.checks = append(g.checks, graphRule{g: gl, checker: f[1]})
		}
		known[f[1]] = true
	case "apply":
		if len(f) < 3 {
			return fmt.Errorf("apply takes a name and a text")
		}
		if _, dup := g.applies[f[1]]; dup {
			return fmt.Errorf("a second apply line for %s", f[1])
		}
		g.applies[f[1]] = strings.Join(f[2:], " ")
		applyLine[f[1]] = n
	case "mode":
		if len(f) != 3 {
			return fmt.Errorf("mode takes a glob and an octal mode")
		}
		gl, err := scope.CompileGlob(f[1])
		if err != nil {
			return err
		}
		m, err := strconv.ParseUint(f[2], 8, 32)
		if err != nil || m > 0o777 || len(f[2]) < 3 || len(f[2]) > 4 {
			return fmt.Errorf("mode %q is not an octal mode up to 0777", f[2])
		}
		g.modes = append(g.modes, graphRule{g: gl, mode: os.FileMode(m)})
	default:
		return fmt.Errorf("unknown keyword %q", f[0])
	}
	return nil
}

// Checker returns the name of the checker that reads p (an absolute clean
// path), or "" when none does.
func (g *Graph) Checker(p string) string {
	for _, r := range g.checks {
		if r.g.Match(p) {
			return r.checker
		}
	}
	return ""
}

// Checkers returns the names of the checkers the graph uses, in file order.
func (g *Graph) Checkers() []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range g.checks {
		if !seen[r.checker] {
			seen[r.checker] = true
			out = append(out, r.checker)
		}
	}
	return out
}

// Files returns the regular files on this machine that a checker reads,
// sorted: what sc check looks at when it is given no path. Symlinks are
// left out, as their content is another file's.
func (g *Graph) Files() []string {
	seen := map[string]bool{}
	for _, r := range g.checks {
		dir := literalDir(r.g.String())
		if dir == "" || dir == "/" {
			continue // a pattern with no fixed directory is not walked
		}
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && r.g.Match(p) && g.Checker(p) == r.checker {
				seen[p] = true
			}
			return nil // an unreadable directory is skipped
		})
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// literalDir returns the directory a pattern's matches all lie in: the
// pattern itself when it has no glob character, "" when it has no fixed
// directory.
func literalDir(pattern string) string {
	i := strings.IndexAny(pattern, `*?[{\`)
	if i < 0 {
		return pattern
	}
	j := strings.LastIndexByte(pattern[:i], '/')
	if j < 0 {
		return "" // "**/name": no fixed directory
	}
	return pattern[:j]
}

// Apply says when a saved change of p takes effect, or "" when the graph
// does not know.
func (g *Graph) Apply(p string) string {
	return g.applies[g.Checker(p)]
}

// Mode returns the mode of a file sc edit creates at p: the first matching
// mode line, else 0644.
func (g *Graph) Mode(p string) os.FileMode {
	for _, r := range g.modes {
		if r.g.Match(p) {
			return r.mode
		}
	}
	return 0o644
}

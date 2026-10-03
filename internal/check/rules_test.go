package check

import (
	"regexp"
	"strings"
	"testing"
)

// Every rule has a unique kebab-case id, a severity and an explanation of
// two to five lines that fit an 80-column console.
func TestRulesTable(t *testing.T) {
	id := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)
	seen := map[string]bool{}
	for _, r := range rules {
		if !id.MatchString(r.ID) || seen[r.ID] {
			t.Errorf("rule id %q: malformed or repeated", r.ID)
		}
		seen[r.ID] = true
		if r.Severity < Warning || r.Severity > Blocker {
			t.Errorf("%s: severity %d", r.ID, r.Severity)
		}
		lines := strings.Split(strings.TrimRight(r.Explain, "\n"), "\n")
		if len(lines) < 2 || len(lines) > 5 {
			t.Errorf("%s: explanation has %d lines", r.ID, len(lines))
		}
		for _, l := range lines {
			if len(l) > 76 {
				t.Errorf("%s: line too long: %q", r.ID, l)
			}
		}
		if got, ok := Lookup(r.ID); !ok || got.ID != r.ID {
			t.Errorf("Lookup(%s) = %+v, %v", r.ID, got, ok)
		}
	}
	if _, ok := Lookup("no-such-rule"); ok {
		t.Error("unknown rule found")
	}
	for s, want := range map[Severity]string{Warning: "warning", Error: "error", Blocker: "blocker", 0: "unknown"} {
		if s.String() != want {
			t.Errorf("Severity(%d) = %s", s, s)
		}
	}
}

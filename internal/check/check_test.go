package check

import (
	"fmt"
	"strings"
	"testing"
)

func TestAdded(t *testing.T) {
	f := func(line int, rule string, sev Severity, text string) Finding {
		return Finding{Rule: rule, Severity: sev, Line: line, Text: text, Path: "/etc/x"}
	}
	old := []Finding{
		f(3, "a-rule", Error, "one"),
		f(5, "a-rule", Error, "two"),
		f(9, "b-rule", Warning, "disk gone"),
	}
	show := func(fs []Finding) string {
		var out []string
		for _, x := range fs {
			out = append(out, fmt.Sprintf("%d %s %s %s", x.Line, x.Rule, x.Severity, x.Text))
		}
		return strings.Join(out, "; ")
	}
	for name, tc := range map[string]struct {
		before, after []Finding
		want          string
	}{
		"nothing changed":           {old, old, ""},
		"lines moved":               {old, []Finding{f(4, "a-rule", Error, "one"), f(6, "a-rule", Error, "two"), f(10, "b-rule", Warning, "disk gone")}, ""},
		"one fixed":                 {old, old[1:], ""},
		"one new":                   {old, append([]Finding{f(1, "c-rule", Blocker, "new")}, old...), "1 c-rule blocker new"},
		"same text, other rule":     {old, []Finding{f(3, "b-rule", Error, "one")}, "3 b-rule error one"},
		"the same finding twice":    {old, []Finding{f(3, "a-rule", Error, "one"), f(7, "a-rule", Error, "one")}, "7 a-rule error one"},
		"severity rose":             {old, []Finding{f(9, "b-rule", Blocker, "disk gone")}, "9 b-rule blocker disk gone"},
		"no findings before":        {nil, old[:1], "3 a-rule error one"},
		"no findings after":         {old, nil, ""},
		"same text, other key":      {[]Finding{{Rule: "a-rule", Severity: Error, Line: 3, Text: "bad line", Key: "k1"}}, []Finding{{Rule: "a-rule", Severity: Error, Line: 1, Text: "bad line", Key: "k2"}}, "1 a-rule error bad line"},
		"same key, moved":           {[]Finding{{Rule: "a-rule", Severity: Error, Line: 3, Text: "bad line", Key: "k1"}}, []Finding{{Rule: "a-rule", Severity: Error, Line: 9, Text: "bad line", Key: "k1"}}, ""},
		"twice before, once after":  {[]Finding{old[0], old[0]}, old[:1], ""},
		"twice before, three after": {[]Finding{old[0], old[0]}, []Finding{old[0], old[0], f(8, "a-rule", Error, "one")}, "8 a-rule error one"},
	} {
		if got := show(Added(tc.before, tc.after)); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
	if Worst(nil) != 0 || Worst(old) != Error || Worst(append(old, f(1, "c-rule", Blocker, "x"))) != Blocker {
		t.Error("Worst")
	}
}

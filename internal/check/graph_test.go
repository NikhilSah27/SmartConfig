package check

import (
	"strings"
	"testing"

	"smartconfig/internal/scope"
)

const testGraph = `
# a comment
check fstab   /etc/fstab
apply fstab   at the next boot (now: systemctl daemon-reload, then mount -a)
check sudoers /etc/sudoers /etc/sudoers.d/*
mode  /etc/sudoers.d/* 0440
check unit    /etc/systemd/system/*.{service,timer}
check late    /etc/sudoers.d/zz   /etc/systemd/**
`

func TestGraph(t *testing.T) {
	g, err := ParseGraph(testGraph)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		"/etc/fstab":                      "fstab",
		"/etc/fstab.d/x":                  "",
		"/etc/sudoers":                    "sudoers",
		"/etc/sudoers.d/90-x":             "sudoers",
		"/etc/sudoers.d/zz":               "sudoers", // the first matching line wins
		"/etc/sudoers.d/sub/x":            "",
		"/etc/systemd/system/a.service":   "unit",
		"/etc/systemd/system/a.mount":     "late",
		"/etc/systemd/system/a.d/x.conf":  "late",
		"/home/u/.ssh/authorized_keys":    "",
		"/etc/systemd/system/a.b.timer":   "unit",
		"/etc/systemd/system/sub/a.timer": "late",
		"/etc/systemd/systemd.conf.d/x":   "late",
		"/etc/systemd":                    "late", // "/**" matches the directory itself
		"/etc/systemdx":                   "",
	} {
		if got := g.Checker(p); got != want {
			t.Errorf("Checker(%s) = %q, want %q", p, got, want)
		}
	}
	if got := strings.Join(g.Checkers(), " "); got != "fstab sudoers unit late" {
		t.Errorf("Checkers() = %q", got)
	}
	if got := g.Apply("/etc/fstab"); got != "at the next boot (now: systemctl daemon-reload, then mount -a)" {
		t.Errorf("Apply = %q", got)
	}
	if g.Apply("/etc/sudoers") != "" || g.Apply("/nowhere") != "" {
		t.Error("Apply for a checker without an apply line, or no checker")
	}
	if g.Mode("/etc/sudoers.d/90-x") != 0o440 || g.Mode("/etc/sudoers") != 0o644 || g.Mode("/etc/fstab") != 0o644 {
		t.Errorf("Mode: %o %o", g.Mode("/etc/sudoers.d/90-x"), g.Mode("/etc/sudoers"))
	}
}

func TestGraphParseErrors(t *testing.T) {
	for text, want := range map[string]string{
		"chek fstab /etc/fstab":                  `line 1: unknown keyword "chek"`,
		"check fstab":                            "line 1: check takes a name and at least one glob",
		"check Fstab /etc/fstab":                 `checker name "Fstab"`,
		"\ncheck fstab etc/fstab":                "line 2: ",
		"check fstab /etc/fstab\napply fstab":    "line 2: apply takes a name and a text",
		"apply fstab soon":                       "line 1: apply fstab: no check line names fstab",
		"check a /a\napply a now\napply a later": "line 3: a second apply line for a",
		"mode /etc/x 0444 extra":                 "line 1: mode takes a glob and an octal mode",
		"mode /etc/x 888":                        `mode "888"`,
		"mode /etc/x 4755":                       `mode "4755"`,
		"mode /etc/x 44":                         `mode "44"`,
		"mode /etc/{x 0444":                      "line 1: ",
	} {
		_, err := ParseGraph(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
}

// graphSamples has, for every checker of the built-in graph, paths it must
// read. Each step that adds a checker adds its lines here.
var graphSamples = map[string][]string{}

// The built-in graph parses, every checker in it has samples, each sample
// is read by its checker, and each is a path the scope records: a checker
// for a file scd never sees would never run from the watcher.
func TestDefaultGraph(t *testing.T) {
	g := DefaultGraph()
	sc := scope.Default()
	for _, name := range g.Checkers() {
		if len(graphSamples[name]) == 0 {
			t.Errorf("checker %s has no sample path in graphSamples", name)
		}
	}
	for name, paths := range graphSamples {
		for _, p := range paths {
			if got := g.Checker(p); got != name {
				t.Errorf("Checker(%s) = %q, want %q", p, got, name)
			}
			if !sc.Recorded(p) {
				t.Errorf("%s has a checker but the scope does not record it", p)
			}
			if sc.FingerprintOnly(p) {
				t.Errorf("%s has a checker but is fingerprint-only", p)
			}
		}
	}
}

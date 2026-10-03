package check

import (
	"os"
	"path/filepath"
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

// Files finds the regular files the graph's patterns match on disk.
func TestGraphFiles(t *testing.T) {
	d := t.TempDir()
	for _, f := range []string{"fstab", "sudoers.d/a", "sudoers.d/b", "sudoers.d/sub/c", "units/x.service", "units/y.mount", "units/deep/z.timer", "other"} {
		os.MkdirAll(filepath.Dir(filepath.Join(d, f)), 0o755)
		os.WriteFile(filepath.Join(d, f), nil, 0o644)
	}
	os.Symlink(filepath.Join(d, "other"), filepath.Join(d, "sudoers.d", "link"))
	g, err := ParseGraph("check fstab " + d + "/fstab " + d + "/missing\ncheck sudoers " + d + "/sudoers.d/*\n" +
		"check unit " + d + "/units/*.{service,timer} " + d + "/units/deep/**\ncheck any **/nowhere")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fstab", "sudoers.d/a", "sudoers.d/b", "units/deep/z.timer", "units/x.service"}
	got := g.Files()
	for i := range got {
		got[i], _ = filepath.Rel(d, got[i])
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Files() = %q, want %q", got, want)
	}
}

// Files with a pattern that has alternatives finds the top-level file and
// not one of the same name deeper down. That the walk stops at the
// pattern's depth is MaxDepth's (scope's TestExportedGlob).
func TestGraphFilesAlternatives(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "grub", "x86_64-efi"), 0o755)
	os.WriteFile(filepath.Join(d, "grub", "grub.cfg"), nil, 0o644)
	os.WriteFile(filepath.Join(d, "grub", "x86_64-efi", "grub.cfg"), nil, 0o644)
	g, err := ParseGraph("check grubcfg " + d + "/grub/{grub.cfg,custom.cfg}\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Files(); len(got) != 1 || got[0] != filepath.Join(d, "grub", "grub.cfg") {
		t.Errorf("Files() = %q", got)
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
var graphSamples = map[string][]string{
	"fstab":    {"/etc/fstab"},
	"sudoers":  {"/etc/sudoers", "/etc/sudoers.d/90-local"},
	"sshd":     {"/etc/ssh/sshd_config", "/etc/ssh/sshd_config.d/50-local.conf"},
	"unit":     {"/etc/systemd/system/my.service", "/etc/systemd/system/my.timer"},
	"shsyntax": {"/etc/default/grub"},
	"grubcfg":  {"/boot/grub/grub.cfg", "/boot/grub/custom.cfg"},
	"nsswitch": {"/etc/nsswitch.conf"},
	"preload":  {"/etc/ld.so.preload"},
	"flag":     {"/etc/nologin", "/etc/ssh/sshd_not_to_be_run"},
	"hosts":    {"/etc/hosts"},
	"netplan":  {"/etc/netplan/60-local.yaml"},
	"udev":     {"/etc/udev/rules.d/70-local.rules"},
}

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

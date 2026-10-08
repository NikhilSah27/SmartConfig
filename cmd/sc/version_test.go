package main

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

// sc version: the package version the build set, the commit and Go's
// version (M5 plan, step 1).
func TestVersion(t *testing.T) {
	r := sc(t, "version")
	if r.code != 0 || !regexp.MustCompile(`^sc devel \(([0-9a-f]{12}(-dirty)?|no commit stamp), go1\.\d+[^)]*\)\n$`).MatchString(r.stdout) {
		t.Errorf("%+v", r)
	}
	for _, tc := range []struct {
		settings []debug.BuildSetting
		want     string
	}{
		{nil, ""},
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "643cec3085330c915ca2c621ff19ef427711cd69"}, {Key: "vcs.modified", Value: "false"}}, "643cec308533"},
		{[]debug.BuildSetting{{Key: "vcs.modified", Value: "true"}, {Key: "vcs.revision", Value: "643cec3085330c915ca2c621ff19ef427711cd69"}}, "643cec308533-dirty"},
		{[]debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, ""},
	} {
		if got := revision(tc.settings); got != tc.want {
			t.Errorf("revision(%v) = %q, want %q", tc.settings, got, tc.want)
		}
	}
}

// scripts/version.sh: 0.N.0 at the tag mN, else after it
// 0.N.99+git<count>.<commit time>.<sha7>, the same for the same commit,
// and later for a later commit in the same second (the M5 review, A3); no
// tag is an error (A8).
func TestVersionScript(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_DATE=2026-10-08T02:35:03Z", "GIT_COMMITTER_DATE=2026-10-08T02:35:03Z",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	script, _ := filepath.Abs("../../scripts/version.sh")
	version := func() (string, error) {
		t.Helper()
		cmd := exec.Command("sh", script)
		cmd.Dir = dir
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return stderr.String(), err
		}
		return strings.TrimSpace(string(out)), nil
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "one")
	if got, err := version(); err == nil || !strings.Contains(got, "no mN tag before HEAD: git fetch --tags") {
		t.Errorf("no tag: %q %v", got, err)
	}
	git("tag", "m4")
	if got, err := version(); got != "0.4.0" || err != nil {
		t.Errorf("at m4: %q %v", got, err)
	}
	git("commit", "-q", "--allow-empty", "-m", "two")
	first := "0.4.99+git1.20261008023503." + git("rev-parse", "--short=7", "HEAD")
	if got, _ := version(); got != first {
		t.Errorf("after m4: %q, want %q", got, first)
	}
	if again, _ := version(); again != first {
		t.Errorf("the same commit again: %q", again)
	}
	// The next commit in the same second, whatever the two shas.
	git("commit", "-q", "--allow-empty", "-m", "three")
	second, _ := version()
	if second != "0.4.99+git2.20261008023503."+git("rev-parse", "--short=7", "HEAD") {
		t.Errorf("the next commit: %q", second)
	}
	if _, err := exec.LookPath("dpkg"); err == nil {
		for _, pair := range [][2]string{{first, second}, {"0.4.0", first}, {second, "0.5.0"}} {
			if err := exec.Command("dpkg", "--compare-versions", pair[0], "lt", pair[1]).Run(); err != nil {
				t.Errorf("%s does not sort before %s", pair[0], pair[1])
			}
		}
	}
}

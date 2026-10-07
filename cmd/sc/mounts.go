package main

import (
	"os"
	"strings"

	"smartconfig/internal/check"
)

// fstabPath and mountinfoPath are where sc reads the mount points a
// machine has and what is mounted now; tests fake them.
var (
	fstabPath     = "/etc/fstab"
	mountinfoPath = "/proc/self/mountinfo"
)

// unmountedMount returns the deepest mount point in fstab that path is
// under, or is, when nothing is mounted there; else "". A file written
// there lands on the filesystem below, which nothing reads once the mount
// is back (a separate /boot: M4 follow-up 1). Without an fstab or a mount
// table to read it cannot tell, and says "".
func unmountedMount(path string) string {
	data, err := os.ReadFile(fstabPath)
	if err != nil {
		return ""
	}
	best := ""
	for _, t := range check.FstabTargets(data) {
		if t != "/" && (path == t || strings.HasPrefix(path, t+"/")) && len(t) > len(best) {
			best = t
		}
	}
	if best == "" || mounted(best) {
		return ""
	}
	return best
}

// mounted reports whether something is mounted at dir, or that it cannot
// be told (no mount table).
func mounted(dir string) bool {
	data, err := os.ReadFile(mountinfoPath)
	if err != nil {
		return true
	}
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Fields(l); len(f) > 4 && check.Unmangle(f[4]) == dir {
			return true
		}
	}
	return false
}

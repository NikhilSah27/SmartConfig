package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"smartconfig/internal/check"
)

// fstabPath and mountinfoPath are where sc reads the mount points a
// machine has and what is mounted now; tests fake them.
var (
	fstabPath     = "/etc/fstab"
	mountinfoPath = "/proc/self/mountinfo"
)

// fstabTargets returns the mount points of fstab, a symlink among them
// resolved as mount(8) and systemd resolve it (the chunk G review: the
// mount table names the real directory); nil without an fstab.
func fstabTargets() []string {
	data, err := os.ReadFile(fstabPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range check.FstabTargets(data) {
		if r, err := filepath.EvalSymlinks(t); err == nil {
			t = r
		}
		out = append(out, t)
	}
	return out
}

// unmountedMounts returns the mount points of fstab that path is under
// and that must be mounted, outermost first, for a write to path to land
// where the boot reads it: those below the deepest one a path reaches
// now. A file written under one that is not mounted lands on the
// filesystem below, which nothing reads once the mount is back (a
// separate /boot: M4 follow-up 1); a mount another one covers since
// (/srv/data, then /srv on top) is not reached (the chunk G review).
// fstab itself is read before any of its mounts: a bad line in it must
// not block its own undo. Without an fstab or a mount table to read it
// cannot tell, and says none.
func unmountedMounts(path string) []string {
	if path == fstabPath {
		return nil
	}
	var under []string
	for _, t := range fstabTargets() {
		if t != "/" && strings.HasPrefix(path, t+"/") {
			under = append(under, t)
		}
	}
	if len(under) == 0 {
		return nil
	}
	table, ok := mountTable()
	if !ok {
		return nil
	}
	sort.Slice(under, func(i, j int) bool { return len(under[i]) < len(under[j]) })
	i := len(under)
	for i > 0 && !table.reached(under[i-1]) {
		i--
	}
	var out []string
	for _, t := range under[i:] {
		if len(out) == 0 || out[len(out)-1] != t {
			out = append(out, t)
		}
	}
	return out
}

// mountCommands is "mount X" for each of mps, joined by sep.
func mountCommands(mps []string, sep string) string {
	cmds := make([]string, len(mps))
	for i, mp := range mps {
		cmds[i] = "mount " + shellQuote(mp)
	}
	return strings.Join(cmds, sep)
}

// fstabMountsFile reports whether path is itself a mount point in fstab
// (a file bind-mounted there): sc cannot put it back in place, mounted or
// not (the chunk G review).
func fstabMountsFile(path string) bool {
	for _, t := range fstabTargets() {
		if t == path {
			return true
		}
	}
	return false
}

// mount is one line of the mount table: its id, its parent's, and where
// it is mounted.
type mount struct{ id, parent, point string }

type mounts []mount

// mountTable reads the mount table; false when it cannot.
func mountTable() (mounts, bool) {
	data, err := os.ReadFile(mountinfoPath)
	if err != nil {
		return nil, false
	}
	var ms mounts
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Fields(l); len(f) > 4 {
			ms = append(ms, mount{f[0], f[1], check.Unmangle(f[4])})
		}
	}
	return ms, len(ms) > 0
}

// reached reports whether a path through dir reaches a mount on dir:
// walked from the root's mount down, each directory's top mount on the
// mount the walk is in. True when the table has no root to start from:
// it cannot tell.
func (ms mounts) reached(dir string) bool {
	ids := make(map[string]bool, len(ms))
	for _, m := range ms {
		ids[m.id] = true
	}
	cur := ""
	for _, m := range ms {
		if m.point == "/" && !ids[m.parent] {
			cur = m.id
			break
		}
	}
	if cur == "" {
		return true
	}
	// on climbs the mounts stacked on point over cur, and says whether
	// there was one.
	on := func(point string) bool {
		moved := false
		for n := 0; n < len(ms); n++ {
			next := ""
			for _, m := range ms {
				if m.point == point && m.parent == cur && m.id != cur {
					next = m.id
					break
				}
			}
			if next == "" {
				break
			}
			cur, moved = next, true
		}
		return moved
	}
	on("/")
	top, p := "/", ""
	for _, c := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		p += "/" + c
		if on(p) {
			top = p
		}
	}
	return top == dir
}

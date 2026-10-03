//go:build linux

package check

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func init() {
	rules = append(rules,
		Rule{"fstab-source-missing", Blocker, `The line names a disk or partition that does not exist on this machine.
At boot systemd waits 90 s for it, fails local-fs.target and stops in
emergency mode; on Ubuntu root is locked, so there is no shell.
Fix the UUID (lsblk -f lists them), or add nofail if the disk may be
absent. With nofail or noauto, or for swap, the boot goes on.`},
		Rule{"fstab-root-source", Error, `The line for / names a device that does not exist on this machine.
Ubuntu most likely still boots, because the initramfs mounts / from the
kernel command line, but the fsck and remount of / use this line.
Fix the UUID: lsblk -f shows the one / is on.`},
		Rule{"fstab-fstype-mismatch", Blocker, `The filesystem type on the line is not the one on the disk, so the
mount fails. For a required disk that stops the boot in emergency mode.
Use the type lsblk -f shows, or auto.`},
		Rule{"fstab-option-typo", Blocker, `The option is not one sc knows and is a letter or two away from a
common one. The kernel refuses a mount with an unknown option; for a
required disk that stops the boot in emergency mode.
Correct the spelling. If the option is real, save anyway.`},
		Rule{"fstab-fields", Error, `The line has fewer than three fields, or a dump or pass field that is
not a number. mount and systemd ignore such a line, so the filesystem
is not mounted.
A line is: device, mount point, type, options, dump, pass.`},
		Rule{"fstab-verify", Warning, `findmnt --verify reports an error that sc has no rule for.
sc check -v shows its message.`},
	)
}

// fstabEntry is one valid line of an fstab.
type fstabEntry struct {
	line                   int
	source, target, fstype string
	opts                   []string // option names, without "=value"
}

// parseFstab splits an fstab into its entries and the numbers of the
// lines libmount would ignore (fewer than three fields, or a dump or pass
// field that is not a number).
func parseFstab(data []byte) (entries []fstabEntry, bad []int) {
	for i, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		ok := len(f) >= 3
		for _, n := range f[min(len(f), 4):min(len(f), 6)] {
			if _, err := strconv.Atoi(n); err != nil {
				ok = false
			}
		}
		if !ok {
			bad = append(bad, i+1)
			continue
		}
		e := fstabEntry{line: i + 1, source: unmangle(f[0]), target: unmangle(f[1]), fstype: f[2]}
		if len(f) > 3 {
			for _, o := range strings.Split(f[3], ",") {
				if name, _, _ := strings.Cut(o, "="); name != "" {
					e.opts = append(e.opts, name)
				}
			}
		}
		entries = append(entries, e)
	}
	return entries, bad
}

// unmangle undoes fstab's octal escapes (\040 is a space).
func unmangle(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

func (e fstabEntry) has(opt string) bool {
	for _, o := range e.opts {
		if o == opt {
			return true
		}
	}
	return false
}

var netFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true,
	"sshfs": true, "fuse.sshfs": true, "ceph": true, "glusterfs": true, "fuse.glusterfs": true,
	"davfs": true, "9p": true, "afs": true, "ncpfs": true}

// severity says how bad a line that cannot be mounted is, from what
// systemd-fstab-generator does with it (plan appendix A14): a local
// filesystem without nofail or noauto is required by local-fs.target,
// whose failure is emergency mode.
func (e fstabEntry) severity() Severity {
	switch {
	case e.has("nofail") || e.has("noauto"):
		return Warning
	case e.target == "/", e.fstype == "swap", netFS[e.fstype], e.has("_netdev"):
		return Error
	}
	return Blocker
}

// deviceExists reports whether source names a device in a form sc can
// check (a /dev path, or a UUID, PARTUUID, LABEL or PARTLABEL tag), and
// whether that device is on this machine.
func (c *Checks) deviceExists(source string) (checked, exists bool) {
	tag, val, isTag := strings.Cut(source, "=")
	val = strings.Trim(val, `"'`)
	if !isTag {
		if strings.HasPrefix(source, "/dev/") {
			return true, c.pathExists(source)
		}
		return false, false
	}
	dir := map[string]string{"UUID": "by-uuid", "PARTUUID": "by-partuuid", "LABEL": "by-label", "PARTLABEL": "by-partlabel"}[tag]
	// udev escapes other characters in the link's name.
	if dir == "" || val == "" || strings.Trim(val, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") != "" {
		return false, false
	}
	for _, v := range []string{val, strings.ToLower(val), strings.ToUpper(val)} {
		if c.pathExists("/dev/disk/" + dir + "/" + v) {
			return true, true
		}
	}
	return true, false
}

var fstypeMismatch = regexp.MustCompile(`^\s+\[W\] (\S+) does not match with on-disk (\S+)`)

// checkFstab checks an fstab: sc's own rules on every line, then findmnt
// --verify for what only it can see (the type on disk, as root).
func checkFstab(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	entries, bad := parseFstab(in.data)
	var out []Finding
	for _, n := range bad {
		out = append(out, Finding{Rule: "fstab-fields", Severity: Error, Line: n,
			Text: "not a valid fstab line; it is ignored, so nothing is mounted"})
	}
	byTarget := map[string]fstabEntry{}
	for _, e := range entries {
		if _, dup := byTarget[e.target]; !dup {
			byTarget[e.target] = e
		}
		if checked, exists := c.deviceExists(e.source); checked && !exists {
			f := Finding{Rule: "fstab-source-missing", Severity: e.severity(), Line: e.line,
				Text: fmt.Sprintf("%s (for %s) is not a device on this machine", e.source, e.target)}
			if e.target == "/" {
				f.Rule = "fstab-root-source"
			}
			out = append(out, f)
		}
		for _, o := range e.opts {
			if near := optionTypo(o); near != "" {
				out = append(out, Finding{Rule: "fstab-option-typo", Severity: e.severity(), Line: e.line,
					Text: fmt.Sprintf("option %q of %s looks like a misspelling of %q", o, e.target, near)})
			}
		}
	}
	res, notes, ok, err := c.validate(ctx, in, "findmnt", "--verify", "--tab-file", in.file)
	if err != nil || !ok {
		return out, notes, err
	}
	target := ""
	for _, l := range strings.Split(string(res.Out), "\n") {
		switch {
		case l == "" || strings.HasPrefix(l, "findmnt: ") || strings.HasPrefix(l, "Success, "):
		case l[0] != ' ' && l[0] != '\t':
			target = l // a section's heading; the summary line is no target
		case fstypeMismatch.MatchString(l):
			m, e := fstypeMismatch.FindStringSubmatch(l), byTarget[target]
			out = append(out, Finding{Rule: "fstab-fstype-mismatch", Severity: e.severity(), Line: e.line, Raw: strings.TrimSpace(l),
				Text: fmt.Sprintf("%s is %s on disk, not %s", target, m[2], m[1])})
		case strings.Contains(l, "[E] ") && !strings.Contains(l, "[E] unreachable on boot required "):
			// Missing sources are sc's own rule above; a missing mount
			// point is no problem, systemd creates it.
			out = append(out, Finding{Rule: "fstab-verify", Severity: Warning, Line: byTarget[target].line, Raw: strings.TrimSpace(l),
				Text: "findmnt reports an error for " + target})
		}
	}
	return out, notes, nil
}

// commonOpts are the options a misspelling is measured against.
var commonOpts = []string{"defaults", "noauto", "nofail", "noatime", "nodiratime", "relatime", "nosuid",
	"nodev", "noexec", "discard", "errors", "users", "owner", "async", "_netdev", "user", "auto", "exec", "sync"}

// knownOpts are options that are real although close to a common one.
var knownOpts = map[string]bool{}

func init() {
	for _, o := range append(commonOpts, strings.Fields(`ro rw suid dev nouser group atime diratime
		norelatime strictatime nostrictatime lazytime nolazytime dirsync mand nomand silent loud
		iversion noiversion symfollow nosymfollow remount bind rbind loop sw pri nodiscard
		acl noacl user_xattr nouser_xattr quota noquota usrquota grpquota prjquota barrier nobarrier
		data commit uid gid umask dmask fmask mode size subvol subvolid compress ssd autodefrag
		degraded noload norecovery inode64 nouuid dax utf8 iocharset codepage shortname flush
		soft hard intr tcp udp proto port vers sec bg fg nolock timeo retrans rsize wsize
		credentials username password domain allow_other default_permissions nonempty context
		lowerdir upperdir workdir hidepid nr_inodes offset sizelimit`)...) {
		knownOpts[o] = true
	}
}

// optionTypo returns the common option that o is probably a misspelling
// of, or "". Options sc knows, x-* options and short ones are never typos.
func optionTypo(o string) string {
	if knownOpts[o] || len(o) < 4 || strings.HasPrefix(strings.ToLower(o), "x-") || o == "comment" {
		return ""
	}
	for _, c := range commonOpts {
		limit := 2
		if len(c) <= 5 {
			limit = 1
		}
		if editDistance(o, c) <= limit {
			return c
		}
	}
	return ""
}

// editDistance is the number of single-letter insertions, deletions,
// replacements and swaps of neighbours that turn a into b.
func editDistance(a, b string) int {
	prev2, prev, cur := []int(nil), make([]int, len(b)+1), make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, make([]int, len(b)+1)
	}
	return prev[len(b)]
}

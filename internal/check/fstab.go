//go:build linux

package check

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func init() {
	rules = append(rules,
		Rule{"fstab-source-missing", Blocker, `The line names a disk, partition, image file or bind-mount source that
does not exist. At boot the mount fails: systemd fails local-fs.target
and usually stops in emergency mode: a shell at the console only.
Fix the UUID (lsblk -f) or the path, or add nofail if it may be absent.
With nofail, noauto or x-systemd.automount, or for swap, the boot goes on.`},
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
		Rule{"fstab-verify", Blocker, `findmnt --verify reports an error that sc has no rule for (a source
tag it does not know, a mount point that is not a directory): the mount
fails, with the same weight as a missing disk on the line.
sc check -v shows its message.`},
	)
}

// fstabEntry is one valid line of an fstab.
type fstabEntry struct {
	line                   int
	source, target, fstype string
	opts                   []string // option names, without "=value"
}

// badLine is a line libmount ignores.
type badLine struct {
	line int
	text string
}

// parseFstab splits an fstab into its entries and the lines libmount would
// ignore (fewer than three fields, or a dump or pass field that is not a
// number).
func parseFstab(data []byte) (entries []fstabEntry, bad []badLine) {
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
			bad = append(bad, badLine{i + 1, l})
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

var octalEscape = regexp.MustCompile(`\\[0-3][0-7]{2}`)

// unmangle undoes fstab's octal escapes (\040 is a space), as libmount
// does for any byte.
func unmangle(s string) string {
	return octalEscape.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseUint(m[1:], 8, 8)
		return string([]byte{byte(n)})
	})
}

var netFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true,
	"sshfs": true, "fuse.sshfs": true, "ceph": true, "glusterfs": true, "fuse.glusterfs": true,
	"davfs": true, "9p": true, "afs": true, "ncpfs": true}

// severity says how bad a line that cannot be mounted is, from what
// systemd-fstab-generator does with it (plan appendix A14): a local
// filesystem without nofail or noauto is required by local-fs.target,
// whose failure is emergency mode. With x-systemd.automount only the
// automount unit is required: the boot goes on and the mount fails when
// first used.
func (e fstabEntry) severity() Severity {
	switch {
	case slices.Contains(e.opts, "nofail") || slices.Contains(e.opts, "noauto"):
		return Warning
	case e.target == "/", e.fstype == "swap", netFS[e.fstype], slices.Contains(e.opts, "_netdev"),
		slices.Contains(e.opts, "x-systemd.automount"):
		return Error
	}
	return Blocker
}

// blockFS are the filesystems that mount a regular file through a loop
// device when the source is a path outside /dev.
var blockFS = map[string]bool{"auto": true, "ext2": true, "ext3": true, "ext4": true, "xfs": true,
	"btrfs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "ntfs-3g": true,
	"iso9660": true, "udf": true, "squashfs": true, "erofs": true, "f2fs": true, "hfsplus": true}

// pathSource says what a source that is a path outside /dev is (a bind
// source, an image file or a swap file), or "" when the line's source is
// not a path sc should look for: a device, a tag, a pseudo or network
// filesystem's name, a fuse source.
func (e fstabEntry) pathSource() string {
	if !strings.HasPrefix(e.source, "/") || strings.HasPrefix(e.source, "/dev/") {
		return ""
	}
	switch {
	case slices.Contains(e.opts, "bind") || slices.Contains(e.opts, "rbind"):
		return "bind source"
	case e.fstype == "swap":
		return "swap file"
	case slices.Contains(e.opts, "loop") || blockFS[e.fstype]:
		return "image file"
	}
	return ""
}

// pathMissing reports whether p surely does not exist: a path sc may not
// look at (not root) is not missing.
func (c *Checks) pathMissing(p string) bool {
	if c.exists != nil {
		return !c.exists(p)
	}
	_, err := os.Stat(p)
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// deviceExists reports whether source names a device in a form sc can
// check (a /dev path, or a UUID, PARTUUID, LABEL or PARTLABEL tag), and
// whether that device is on this machine. Tags are compared as written:
// libblkid and udev's links are case-sensitive.
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
	// udev escapes other characters in the link's name: findmnt decides.
	if dir == "" || val == "" || strings.Trim(val, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") != "" {
		return false, false
	}
	return true, c.pathExists("/dev/disk/" + dir + "/" + val)
}

// sameFS reports whether a line of type want mounts a disk blkid calls
// have, although findmnt's plain comparison says they differ; maybe says
// the mount may still work (an ext3 line on an ext4 disk without ext4-only
// features).
func sameFS(want, have string) (same, maybe bool) {
	ext := map[string]bool{"ext2": true, "ext3": true, "ext4": true}
	for _, w := range strings.Split(want, ",") { // a list: mount tries each
		switch {
		case w == have, strings.HasPrefix(w, "fuse"), w == "ext4" && ext[have],
			have == "ntfs" && (w == "ntfs-3g" || w == "ntfs3" || w == "lowntfs-3g"),
			have == "vfat" && w == "msdos":
			return true, false
		case ext[w] && ext[have]:
			maybe = true
		}
	}
	return false, maybe
}

var (
	fstypeMismatch = regexp.MustCompile(`^\s+\[W\] (\S+) does not match with on-disk (\S+)`)
	sourceMissing  = regexp.MustCompile(`^\s+\[E\] unreachable on boot required source: `)
)

// checkFstab checks an fstab: sc's own rules on every line, then findmnt
// --verify for what only it can see: the type on disk (as root), and
// devices named in a form sc cannot look up.
func checkFstab(ctx context.Context, c *Checks, in input) ([]Finding, []string, error) {
	entries, bad := parseFstab(in.data)
	var out []Finding
	for _, b := range bad {
		out = append(out, Finding{Rule: "fstab-fields", Severity: Error, Line: b.line, Key: lineKey(in.data, b.line),
			Text: "not a valid fstab line; it is ignored, so nothing is mounted"})
	}
	byTarget := map[string][]fstabEntry{}
	flagged := map[int]bool{} // lines with a missing-source finding
	missing := func(e fstabEntry, raw string) Finding {
		f := Finding{Rule: "fstab-source-missing", Severity: e.severity(), Line: e.line, Raw: raw,
			Text: fmt.Sprintf("%s (for %s) is not a device on this machine", e.source, e.target)}
		if e.target == "/" {
			f.Rule = "fstab-root-source"
		}
		flagged[e.line] = true
		return f
	}
	for _, e := range entries {
		byTarget[e.target] = append(byTarget[e.target], e)
		if checked, exists := c.deviceExists(e.source); checked && !exists {
			out = append(out, missing(e, ""))
		}
		// A path below another line's mount point may only exist once
		// that is mounted (a new disk): not looked for.
		if what := e.pathSource(); what != "" && !belowMount(e.source, entries) && c.pathMissing(e.source) {
			f := missing(e, "")
			f.Text = fmt.Sprintf("%s (%s for %s) does not exist", e.source, what, e.target)
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
	for _, l := range strings.Split(string(res.Err), "\n") {
		// Its parse errors are fstab-fields above; anything else it says
		// about itself means it did not check the file.
		if msg, isOwn := strings.CutPrefix(l, "findmnt: "); isOwn && !strings.Contains(msg, "parse error at line") {
			return out, append(notes, in.say("findmnt could not check the file; only sc's own rules ran", "findmnt: "+strings.ReplaceAll(msg, in.file, in.path))), nil
		}
	}
	// stdout is a heading (the mount point) for each line findmnt has
	// something to say about, in the file's order, each followed by its
	// indented messages. Several lines can share a mount point (two swap
	// lines, both "none"; M3 follow-up 8): when each has a heading, the
	// k-th heading is the k-th of them; else a message that names one
	// line's source picks it; else sc cannot tell, and takes the first
	// line and the worst case. A heading sc cannot match gets no line.
	type group struct {
		target string
		msgs   []string
	}
	groups := []group{{}} // messages before any heading: no line
	headings := map[string]int{}
	for _, l := range strings.Split(string(res.Out), "\n") {
		switch {
		case l == "" || strings.HasPrefix(l, "Success, "):
		case l[0] != ' ' && l[0] != '\t':
			groups = append(groups, group{target: l})
			headings[l]++
		default:
			groups[len(groups)-1].msgs = append(groups[len(groups)-1].msgs, l)
		}
	}
	seen := map[string]int{}
	unread := false
	for _, g := range groups {
		target, about := g.target, byTarget[g.target]
		k := seen[target]
		seen[target]++
		if len(about) > 1 && headings[target] == len(about) {
			about = about[k : k+1]
		} else if len(about) > 1 {
			named := slices.DeleteFunc(slices.Clone(about), func(e fstabEntry) bool {
				return !slices.ContainsFunc(g.msgs, func(m string) bool { return namesSource(m, e.source) })
			})
			if len(named) == 1 {
				about = named
			}
		}
		out, unread = fstabMessages(g.msgs, target, about, flagged, missing, out, unread)
	}
	if unread {
		notes = append(notes, "findmnt could not read the disks (not root): filesystem types were not compared")
	}
	return out, notes, nil
}

// namesSource reports whether a findmnt message names source: a path is
// followed by ": reason", a tag (UUID=...) ends the message, as findmnt
// 2.39.3 prints them, and without the quotes fstab may put around a tag's
// value (review of chunk F, A7).
func namesSource(msg, source string) bool {
	src := " source: " + strings.ReplaceAll(source, `"`, "")
	msg = strings.TrimRight(msg, " \t")
	for i := strings.Index(msg, src); i >= 0; {
		if rest := msg[i+len(src):]; rest == "" || rest[0] == ':' {
			return true
		}
		j := strings.Index(msg[i+1:], src)
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return false
}

// fstabMessages reads findmnt's messages under one heading, about the
// lines of about, and adds its findings to out.
func fstabMessages(msgs []string, target string, about []fstabEntry, flagged map[int]bool, missing func(fstabEntry, string) Finding,
	out []Finding, unread bool) ([]Finding, bool) {
	for _, l := range msgs {
		raw := strings.TrimSpace(l)
		f := Finding{Raw: raw, Severity: Error} // a heading sc cannot match: not known to be required
		if len(about) > 0 {
			f.Line, f.Severity = about[0].line, 0
			for _, e := range about {
				f.Severity = max(f.Severity, e.severity())
			}
		}
		switch {
		case fstypeMismatch.MatchString(l):
			m := fstypeMismatch.FindStringSubmatch(l)
			same, maybe := sameFS(m[1], m[2])
			if same {
				continue
			}
			if maybe {
				f.Severity = Warning
			}
			f.Rule, f.Text = "fstab-fstype-mismatch", fmt.Sprintf("%s is %s on disk, not %s", target, m[2], m[1])
		case sourceMissing.MatchString(l):
			// sc's own rule already has the lines it could look up.
			if len(about) > 0 && !slices.ContainsFunc(about, func(e fstabEntry) bool { return !flagged[e.line] }) {
				continue
			}
			if len(about) == 1 {
				f = missing(about[0], raw)
			} else {
				f.Rule, f.Text = "fstab-source-missing", "the device for "+target+" is not on this machine"
			}
		case strings.Contains(l, "cannot detect on-disk filesystem type (Permission denied)"):
			unread = true
			continue
		case strings.HasPrefix(raw, "[E] ") && !strings.HasPrefix(raw, "[E] unreachable on boot required target"):
			// A missing mount point is no problem: systemd creates it.
			// Anything else it calls an error fails the mount: the
			// line's own severity.
			f.Rule, f.Key, f.Text = "fstab-verify", raw, "findmnt reports an error for "+target
		default:
			continue
		}
		out = append(out, f)
	}
	return out, unread
}

// belowMount reports whether p lies below the mount point of another line
// (not /).
func belowMount(p string, entries []fstabEntry) bool {
	for _, e := range entries {
		if e.target != "/" && e.target != "none" && strings.HasPrefix(p, strings.TrimSuffix(e.target, "/")+"/") {
			return true
		}
	}
	return false
}

// commonOpts are the options a misspelling is measured against.
var commonOpts = []string{"defaults", "noauto", "nofail", "noatime", "nodiratime", "relatime", "nosuid",
	"nodev", "noexec", "discard", "errors", "users", "owner", "async", "_netdev", "user", "auto", "exec", "sync"}

//go:embed mountopts.txt
var mountOptsText string

// knownOpts are the real mount options (mountopts.txt): none of them is
// ever called a misspelling.
var knownOpts = map[string]bool{}

func init() {
	for _, l := range strings.Split(mountOptsText, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			knownOpts[l] = true
		}
	}
	for _, o := range commonOpts {
		knownOpts[o] = true
	}
}

// optionTypo returns the common option that o is one slip away from (a
// letter added, dropped, replaced, or two swapped), or "". Options sc
// knows, x-* options and short ones are never typos: the list cannot hold
// every option of every filesystem, so only the nearest misses count.
func optionTypo(o string) string {
	if knownOpts[o] || len(o) < 4 || strings.HasPrefix(strings.ToLower(o), "x-") || o == "comment" {
		return ""
	}
	for _, c := range commonOpts {
		if editDistance(o, c) == 1 {
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

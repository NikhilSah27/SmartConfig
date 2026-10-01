package scope

import (
	"bufio"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// machine is the default scope as scd would use it on a host with the
// login accounts root and u.
func machine() *Scope {
	return Default().With("/var/lib/smartconfig", []string{"/root/.ssh", "/home/u/.ssh"})
}

func TestDefaultParses(t *testing.T) {
	s := Default()
	if got := strings.Join(s.Roots(), " "); got != "/etc /boot/grub" {
		t.Fatalf("roots %q", got)
	}
	if len(s.rules) < 70 {
		t.Fatalf("only %d rules", len(s.rules))
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct{ text, err string }{
		{"root /etc\nfrobnicate /x", "scope line 2: unknown keyword"},
		{"root /etc\nexclude", "scope line 2: exclude takes 1 field(s), got 0"},
		{"root /etc\nexclude /a /b", "exclude takes 1 field(s), got 2"},
		{"root /etc\ntier 1", "tier takes 2 field(s), got 1"},
		{"root /etc\ntier 5 /etc/x", `tier "5" is not 1 to 4`},
		{"root /etc\ntier one /etc/x", `tier "one" is not 1 to 4`},
		{"root etc", `root "etc" is not a clean absolute directory`},
		{"root /etc/", `root "/etc/" is not a clean absolute directory`},
		{"root /", `root "/" is not a clean absolute directory`},
		{"root /etc\n\n# c\nexclude /etc/snap[-.]*", `scope line 4: pattern "/etc/snap[-.]*": segment`},
		{"# no roots\nexclude /etc/x", "scope has no root"},
	} {
		_, err := Parse(c.text)
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%q: %v, want %q", c.text, err, c.err)
		}
	}
}

// The rules of Recorded, on a small scope.
func TestRecordedRules(t *testing.T) {
	s, err := Parse(`
root /r
exclude /r
include /r/skip/keep
exclude /r/skip
exclude /r/**/*.tmp
`)
	if err != nil {
		t.Fatal(err)
	}
	s = s.With("/r/data", []string{"/h/u/.ssh", "relative/.ssh", "/"})
	for p, want := range map[string]bool{
		"/r":             true,  // a root itself is never excluded
		"/r/x":           true,  // "exclude /r" matches only /r
		"/r/skip":        false, // excluded directory
		"/r/skip/keep":   false, // included, but under an excluded directory
		"/r/skip/other":  false,
		"/r/a.tmp":       false,
		"/r/a.tmp/b":     false, // under an excluded directory
		"/r/data":        false, // sc's own data directory
		"/r/data/c.db":   false,
		"/r/datax":       true,
		"/r2/x":          false, // outside every root
		"/rx":            false,
		"/h/u/.ssh/k":    true, // login root added by With
		"/h/u/.sshx":     false,
		"/h/u":           false,
		"/r/../r/x":      false, // not clean
		"/r//x":          false,
		"/r/x/":          false,
		"r/x":            false,
		"":               false,
		"/":              false,
		"relative/.ssh/": false,
	} {
		if got := s.Recorded(p); got != want {
			t.Errorf("Recorded(%q) = %v, want %v", p, got, want)
		}
	}
	if got := strings.Join(s.Roots(), " "); got != "/r /h/u/.ssh" {
		t.Errorf("roots %q", got)
	}
	// With returns a copy.
	s.Roots()[0] = "/changed"
	if s.Roots()[0] != "/r" {
		t.Error("Roots exposes the scope's slice")
	}
	if Default().Recorded("/root/.ssh/authorized_keys") {
		t.Error("With changed the default scope")
	}
}

// Scopes made by With from one base do not share their roots, even when
// the base's slice has room to grow (3 roots: capacity 4).
func TestWithCopiesRoots(t *testing.T) {
	s, err := Parse("root /a\nroot /b\nroot /c")
	if err != nil {
		t.Fatal(err)
	}
	x := s.With("", []string{"/x"})
	y := s.With("", []string{"/y"})
	if !x.Recorded("/x/f") || x.Recorded("/y/f") || !y.Recorded("/y/f") || s.Recorded("/x/f") {
		t.Fatalf("roots: base %v, x %v, y %v", s.Roots(), x.Roots(), y.Roots())
	}
}

// A deep path under an excluded directory is decided at that directory.
// A user can make ~/.ssh/a/a/.../a up to PATH_MAX; walking up from the leaf
// took 8 s at depth 1,000 (chunk C review D3).
func TestRecordedDeepExcludedPath(t *testing.T) {
	s := machine()
	deep := "/home/u/.ssh/a" + strings.Repeat("/a", 2000)
	start := time.Now()
	if s.Recorded(deep) {
		t.Fatal("recorded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

// The synthetic names of plan 7.4 and appendix S7/S15, block D0 included.
func TestDefaultScopeSynthetic(t *testing.T) {
	s := machine()
	kept := []string{
		"/etc/grub.d/10_linux.bak", "/etc/grub.d/40_custom.orig", "/etc/grub.d/10_linux.disabled",
		"/etc/initramfs-tools/hooks/foo.dpkg-old", "/etc/initramfs-tools/hooks/foo.dpkg-new",
		"/etc/apparmor.d/p.dpkg-tmp", "/etc/apparmor.d/p.dpkg-backup", "/etc/apparmor.d/p.ucf-new",
		"/etc/apparmor.d/p.bak",
		"/etc/sudoers.d/sedAbC123", "/etc/sudoers.d/XXab12cd", "/etc/sudoers.d/README",
		"/etc/logrotate.d/rsyslog.dpkg-new",
		"/etc/tmpfiles.d/x.conf",
		"/etc/dconf/db/ibus.d/00-upstream-settings",
		"/etc/systemd/system/multi-user.target.wants/snapd.apparmor.service",
		"/boot/grub", "/boot/grub/grub.cfg", "/boot/grub/custom.cfg",
		"/root/.ssh/authorized_keys", "/home/u/.ssh/authorized_keys", "/home/u/.ssh/authorized_keys2",
		"/etc/ssh/ssh_host_ed25519_key", "/etc/machine-id",
		"/etc", "/etc/fstab", "/etc/hosts", "/etc/ssh/sshd_config",
		// Recorded since 2026-09-30 (plan Appendix C): alternatives links,
		// and private keys and secrets as fingerprints.
		// Consumers that run names the noise rules would hide (chunk C
		// review D2): systemd generators and run-parts directories.
		"/etc/systemd/system-generators/foo.disabled", "/etc/systemd/system-generators/foo.orig",
		"/etc/systemd/system-generators/foo.tmp", "/etc/systemd/system-generators/sedAbC123",
		"/etc/systemd/user-environment-generators/x.distUpgrade",
		"/etc/network/if-up.d/sedAbC123", "/etc/update-motd.d/XXabcdef", "/etc/cron.yearly/sedAbC123",
		"/etc/ppp/ip-up.d/XXab12cd", "/etc/dhcp/dhclient-exit-hooks.d/sedAbC123",
		// Added 2026-09-30 (plan Appendix C9): sshd's per-user rc and
		// environment, and more secrets as fingerprints.
		"/home/u/.ssh/rc", "/home/u/.ssh/environment", "/etc/ssh/sshrc",
		"/etc/dropbear/initramfs/dropbear_ed25519_host_key", "/etc/wireguard/wg0.conf",
		"/etc/apt/auth.conf.d/private.conf", "/etc/cryptsetup-keys.d/data.key",
		"/etc/alternatives/iptables", "/etc/alternatives/editor",
		"/etc/ssl/private/ssl-cert-snakeoil.key", "/etc/ppp/chap-secrets", "/etc/credstore/k",
	}
	excluded := []string{
		"/etc/grub.d/10_linux.dpkg-new", "/etc/grub.d/10_linux.dpkg-old", "/etc/grub.d/10_linux~",
		"/etc/apparmor.d/usr.bin.foo.dpkg-new", "/etc/apparmor.d/abstractions/base.dpkg-dist",
		"/etc/apparmor.d/p.dpkg-remove", "/etc/apparmor.d/p~", "/etc/apparmor.d/p.orig",
		"/etc/sudoers.d/x.dpkg-new", "/etc/sudoers.d/x~", "/etc/sudoers.d/.README.swp",
		"/etc/apt/apt.conf.d/01autoremove.dpkg-new", "/etc/apt/apt.conf.d/20auto-upgrades.ucf-dist",
		"/etc/apt/apt.conf.d/4913",
		"/etc/cron.daily/logrotate.dpkg-new", "/etc/kernel/postinst.d/zz-update-grub.dpkg-new",
		"/etc/.README.swp", "/etc/.hosts.sc-tmp-123", "/etc/grub.d/.40_custom.sc-tmp-9",
		"/etc/sedAbC123",
		"/etc/passwd-", "/etc/passwd.lock", "/etc/shadow.1234", "/etc/.pwd.lock",
		"/boot/grub/grubenv", "/boot/grub/i386-pc/normal.mod", "/boot/grub/i386-pc",
		"/home/u/.ssh/id_ed25519", "/home/u/.ssh/known_hosts", "/root/.ssh/id_ed25519",
		// A directory named like an sshd file adds nothing (chunk D review).
		"/home/u/.ssh/authorized_keys/a", "/home/u/.ssh/authorized_keys/a/etc/shadow",
		"/home/u/.ssh/rc/x", "/root/.ssh/environment/deep/er",
		"/etc/tmpxdvph4n_/resolv.conf", "/etc/dconf/db/ibus",
		"/etc/systemd/system/snap-firefox-1.mount", "/etc/udev/rules.d/70-snap.firefox.rules",
		"/etc/ld.so.cache", "/etc/ssl/certs/ca-certificates.crt", "/etc/security/opasswd",
		"/etc/brlapi.key", "/etc/.git/config",
		"/etc/systemd/system-generators/foo.bak", "/etc/systemd/system-generators/foo.dpkg-new",
		"/etc/systemd/system-generators/foo~", "/etc/systemd/system-generators/.hidden",
		"/etc/systemd/system-generators/foo.rpmsave", "/etc/systemd/user-generators/foo.new",
		"/etc/network/if-up.d/x.dpkg-old", "/etc/update-motd.d/10-x~", "/etc/cron.yearly/x.ucf-dist",
		"/etc/sedAbC123/x", "/etc/.goutputstream-ABC123",
		"/var/log/syslog", "/var/lib/smartconfig/changes.db", "/boot/vmlinuz", "/home/u/.bashrc",
	}
	for _, p := range kept {
		if !s.Recorded(p) {
			_, line := s.decide(p)
			t.Errorf("%s is not recorded (line %d)", p, line)
		}
	}
	for _, p := range excluded {
		if s.Recorded(p) {
			t.Errorf("%s is recorded", p)
		}
	}
}

// tier12 holds files whose loss or corruption can stop the machine booting
// or lock its owner out. Each must be recorded, at tier 1 or 2.
var tier12 = []string{
	"/etc/fstab", "/etc/crypttab", "/etc/default/grub", "/etc/default/grub.d/50-cloudimg.cfg",
	"/etc/grub.d/10_linux", "/etc/grub.d/40_custom", "/boot/grub/grub.cfg",
	"/etc/initramfs-tools/initramfs.conf", "/etc/initramfs-tools/modules",
	"/etc/modprobe.d/blacklist.conf", "/etc/modules", "/etc/modules-load.d/modules.conf",
	"/etc/sysctl.conf", "/etc/sysctl.d/99-sysctl.conf", "/etc/ld.so.conf", "/etc/ld.so.conf.d/libc.conf",
	"/etc/ld.so.preload", "/etc/machine-id",
	"/etc/systemd/system.conf", "/etc/systemd/system/multi-user.target.wants/ssh.service",
	"/etc/systemd/system/getty.target.wants/getty@tty1.service",
	"/etc/udev/rules.d/99-local.rules", "/etc/apparmor.d/tunables/global",
	"/etc/sudoers", "/etc/sudoers.d/README", "/etc/sudo.conf",
	"/etc/passwd", "/etc/group", "/etc/shadow", "/etc/gshadow", "/etc/nsswitch.conf",
	"/etc/pam.d/common-auth", "/etc/pam.d/sshd", "/etc/pam.d/sudo", "/etc/security/limits.conf",
	"/etc/login.defs", "/etc/shells", "/etc/environment", "/etc/profile", "/etc/bash.bashrc",
	"/etc/ssh/sshd_config", "/etc/ssh/sshd_config.d/50-cloud-init.conf", "/etc/ssh/ssh_host_rsa_key",
	"/etc/systemd/logind.conf", "/etc/polkit-1/rules.d/50-default.rules",
	"/root/.ssh/authorized_keys", "/home/u/.ssh/authorized_keys2",
}

func TestTier12NeverExcluded(t *testing.T) {
	s := machine()
	for _, p := range tier12 {
		if !s.Recorded(p) {
			_, line := s.decide(p)
			t.Errorf("%s is excluded by line %d", p, line)
		}
		if tr := s.Tier(p); tr > 2 {
			t.Errorf("%s is tier %d", p, tr)
		}
	}
}

func TestTierAndFingerprint(t *testing.T) {
	s := machine()
	for _, c := range []struct {
		path   string
		tier   int
		digest bool
	}{
		{"/etc/fstab", 1, false},
		{"/etc/machine-id", 1, true},
		{"/boot/grub/grub.cfg", 1, false},
		{"/etc/grub.d/10_linux", 1, false},
		{"/etc/systemd/system/multi-user.target.wants/ssh.service", 1, false},
		{"/etc/sudoers", 2, false},
		{"/etc/ssh/sshd_config", 2, false},
		{"/etc/ssh/ssh_host_rsa_key", 2, true},
		{"/etc/ssh/ssh_host_ed25519_key", 2, true},
		{"/etc/ssh/ssh_host_ecdsa_key", 2, true},
		{"/etc/ssh/ssh_host_rsa_key.pub", 4, false},
		// ssh-keygen -A's temp names (OpenSSH 9.6: "%s%s.XXXXXXXXXX") and
		// the protocol 1 key.
		{"/etc/ssh/ssh_host_ed25519_key.AbCdEfGhIj", 2, true},
		{"/etc/ssh/ssh_host_key", 2, true},
		{"/etc/ssh/ssh_host_key.AbCdEfGhIj", 2, true},
		{"/etc/ssh/ssh_host_rsa_key.pub.AbCdEfGhIj", 4, false},
		{"/etc/ssh/ssh_host_rsa_key.AbCdEfGhI", 4, false},
		{"/home/u/.ssh/authorized_keys", 2, false},
		{"/etc/hosts", 3, false},
		{"/etc/resolv.conf", 3, false},
		{"/etc/netplan/01-network-manager-all.yaml", 3, false},
		{"/etc/apt/sources.list", 3, false},
		{"/etc/hostname", 4, false},
		{"/etc/ssh/ssh_config", 4, false},
		{"/etc/logrotate.d/rsyslog", 4, false},
		{"/etc/ssh/ssh_host_new_key", 2, true},
		{"/etc/ssl/private/ssl-cert-snakeoil.key", 2, true},
		{"/etc/credstore.encrypted/db.cred", 2, true},
		{"/etc/ppp/pap-secrets", 2, true},
		{"/etc/ppp/options", 4, false},
		{"/home/u/.ssh/rc", 2, false},
		{"/home/u/.ssh/environment", 2, false},
		{"/etc/ssh/sshrc", 2, false},
		{"/etc/dropbear/initramfs/dropbear_rsa_host_key", 2, true},
		{"/etc/dropbear-initramfs/dropbear_ecdsa_host_key", 2, true},
		{"/etc/dropbear/dropbear_ed25519_host_key", 2, true},
		{"/etc/dropbear/initramfs/dropbear.conf", 2, false},
		{"/etc/dropbear/initramfs/authorized_keys", 2, false},
		{"/etc/wireguard/wg0.conf", 3, true},
		{"/etc/wireguard/private.key", 3, true},
		{"/etc/apt/auth.conf", 3, true},
		{"/etc/apt/auth.conf.d/private.conf", 3, true},
		{"/etc/apt/sources.list.d/ubuntu.sources", 3, false},
		{"/etc/cryptsetup-keys.d/data.key", 1, true},
		{"/etc/alternatives/iptables", 3, false},
		{"/etc/alternatives/ip6tables-restore", 3, false},
		{"/etc/alternatives/ebtables-save", 3, false},
		{"/etc/alternatives/editor", 4, false},
	} {
		if got := s.Tier(c.path); got != c.tier {
			t.Errorf("Tier(%s) = %d, want %d", c.path, got, c.tier)
		}
		if got := s.FingerprintOnly(c.path); got != c.digest {
			t.Errorf("FingerprintOnly(%s) = %v, want %v", c.path, got, c.digest)
		}
	}
}

// Paths are cleaned where a wrong answer would store sc's own data or a
// secret (chunk C review D4, D5). Recorded itself takes only clean paths.
func TestUncleanPaths(t *testing.T) {
	for _, h := range []string{"/etc/sc/", "/etc//sc", "/etc/x/../sc"} {
		if Default().With(h, nil).Recorded("/etc/sc/changes.db") {
			t.Errorf("With(%q): sc's data recorded", h)
		}
	}
	for _, h := range []string{"etc/sc", "", "/"} {
		if got := Default().With(h, nil).scHome; got != "" {
			t.Errorf("With(%q) excludes %q", h, got)
		}
	}
	if got := strings.Join(Default().With("", []string{"/home/u/.ssh/", "/home/u//.ssh"}).Roots(), " "); got != "/etc /boot/grub /home/u/.ssh" {
		t.Errorf("roots %q", got)
	}
	s := machine()
	for _, p := range []string{"/etc/ssh//ssh_host_rsa_key", "/etc/ssh/./ssh_host_rsa_key", "/etc/x/../ssh/ssh_host_rsa_key",
		"etc/ssh/ssh_host_rsa_key", "relative", ""} {
		if !s.FingerprintOnly(p) {
			t.Errorf("FingerprintOnly(%q) = false", p)
		}
	}
	if s.FingerprintOnly("/etc//hosts") {
		t.Error("FingerprintOnly(/etc//hosts)")
	}
	if got := s.Tier("/etc//fstab"); got != 1 {
		t.Errorf("Tier(/etc//fstab) = %d", got)
	}
}

// Fingerprint-only holds even where nothing is recorded, so a manual
// snapshot of such a path is a fingerprint too.
func TestFingerprintOnlyOutsideRecordedScope(t *testing.T) {
	s, err := Parse("root /r\nexclude /r/secret/**\ndigest /r/secret/key\ndigest /x/key")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/r/secret/key", "/x/key"} {
		if s.Recorded(p) || !s.FingerprintOnly(p) {
			t.Errorf("%s: recorded %v, fingerprint only %v", p, s.Recorded(p), s.FingerprintOnly(p))
		}
	}
}

func TestLoginHomes(t *testing.T) {
	b, err := os.ReadFile("testdata/passwd")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(LoginHomes(b), " ")
	if want := "/root /home/alice /home/bob /home/emptyshell"; got != want {
		t.Fatalf("homes %q, want %q", got, want)
	}
	if LoginHomes(nil) != nil {
		t.Fatal("homes from no passwd")
	}
	// CRLF line ends and trailing spaces around the shell.
	crlf := "a:x:1:1::/home/a:/usr/sbin/nologin\r\nb:x:2:2::/home/b:/bin/false \r\nc:x:3:3::/home/c:/bin/bash\r\n"
	if got := strings.Join(LoginHomes([]byte(crlf)), " "); got != "/home/c" {
		t.Fatalf("CRLF homes %q, want /home/c", got)
	}
}

// A path under nested roots is judged from the innermost one: a login
// root inside an excluded directory is still watched.
func TestNestedRoots(t *testing.T) {
	s, err := Parse("root /r\nexclude /r/skip")
	if err != nil {
		t.Fatal(err)
	}
	s = s.With("", []string{"/r/skip/u/.ssh"})
	if !s.Recorded("/r/skip/u/.ssh/authorized_keys") || s.Recorded("/r/skip/u/x") {
		t.Fatal("nested root judged from the outer root")
	}
}

// snapRuntime matches the snapd runtime names under tier 1-2 directories
// that the default scope excludes on purpose (plan 7.4).
var snapRuntime = []string{
	"/etc/systemd/system/snap-*.mount",
	"/etc/systemd/system/snap.*",
	"/etc/systemd/system/*.wants/snap-*",
	"/etc/systemd/system/*.wants/snap.*",
	"/etc/systemd/system/snapd.mounts.target.wants/**",
	"/etc/systemd/user/snap.*",
	"/etc/udev/rules.d/70-snap.*.rules",
	"/etc/security/opasswd",
}

// TestDefaultScopeRealListing checks the default scope against a real
// listing: the output of `find /etc /boot/grub -xdev -printf '%y %p\n'` in
// the file $SC_ETC_LISTING (acceptance step 1). Skipped when it is unset.
func TestDefaultScopeRealListing(t *testing.T) {
	file := os.Getenv("SC_ETC_LISTING")
	if file == "" {
		t.Skip("SC_ETC_LISTING is not set")
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var allow []*glob
	for _, p := range snapRuntime {
		allow = append(allow, mustGlob(t, p))
	}
	s := Default().With("/var/lib/smartconfig", nil)
	var dirs, files, links int
	tiers := map[int]int{}
	var digests, unexpected []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		kind, p, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		if !s.Recorded(p) {
			if kind != "d" && anyTier12(s, p) && !matchAny(allow, p) {
				unexpected = append(unexpected, p)
			}
			continue
		}
		switch kind {
		case "d":
			dirs++
			continue
		case "f":
			files++
		case "l":
			links++
		default:
			continue
		}
		tiers[s.Tier(p)]++
		if s.FingerprintOnly(p) {
			digests = append(digests, p)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(digests)
	t.Logf("watched dirs %d, recorded files %d, links %d", dirs, files, links)
	t.Logf("tier 1: %d, tier 2: %d, tier 3: %d, tier 4: %d", tiers[1], tiers[2], tiers[3], tiers[4])
	t.Logf("fingerprint only: %s", strings.Join(digests, " "))
	for _, p := range unexpected {
		t.Errorf("tier 1-2 path excluded: %s", p)
	}
	// This VM had 1,377 (plan 7.4, without the two authorized_keys), and
	// 1,527 with the alternatives and secrets of Appendix C; the range
	// allows for package changes, not for a rule gone wrong.
	if kept := files + links; kept < 1000 || kept > 2500 {
		t.Errorf("%d files and links recorded, want 1000 to 2500", kept)
	}
	var secrets []*glob
	for _, p := range []string{"/etc/machine-id", "/etc/ssh/ssh_host_*", "/etc/ssl/private/**",
		"/etc/{credstore,credstore.encrypted}/**", "/etc/ppp/*-secrets",
		"/etc/{dropbear,dropbear-initramfs}/**", "/etc/wireguard/**", "/etc/apt/auth.conf{,.d/**}",
		"/etc/cryptsetup-keys.d/**"} {
		secrets = append(secrets, mustGlob(t, p))
	}
	for _, p := range digests {
		if !matchAny(secrets, p) {
			t.Errorf("unexpected fingerprint-only path %s", p)
		}
	}
}

func mustGlob(t *testing.T, p string) *glob {
	t.Helper()
	g, err := compileGlob(p)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func matchAny(gs []*glob, p string) bool {
	for _, g := range gs {
		if g.match(p) {
			return true
		}
	}
	return false
}

// anyTier12 reports whether any tier 1 or 2 line matches p, whatever the
// order of the lines.
func anyTier12(s *Scope, p string) bool {
	for _, r := range s.rules {
		if r.verb == "tier" && r.tier <= 2 && r.g.match(p) {
			return true
		}
	}
	return false
}

#!/bin/sh
# install.sh: the M4 lab installs SmartConfig in its guest with it (boot
# 0, check 0.4); lab only, never shipped. The host copies a directory of
# files to the guest and runs, as root:
#
#   sudo sh DIR/install.sh
#
# DIR holds MANIFEST (sha256sum's format, names relative to DIR) and the
# files under their repo names: sc (bin/sc), 42_smartconfig, 41_sclab,
# 43_sclab, scd.service, sc-boot-seen.service, sc-boot-ok.service and
# smartconfig-rescue.conf. Each must be in MANIFEST and match it. They are
# installed as the README says (root:root; 0755 for sc and the grub.d
# scripts, 0644 for the units and the drop-in, which goes in as
# rescue.service.d/ and emergency.service.d/50-smartconfig.conf). Then
# daemon-reload, enable sc-boot-seen and sc-boot-ok (not --now: they are
# for the next boot), enable --now scd, update-grub, grub-script-check of
# the grub.cfg it wrote, and systemd-analyze verify of the units.
#
# It prints facts, one key=value per line; a key may repeat (a list, in
# order). It decides nothing: lab/e2e.py judges the facts. The keys:
#
#   uid=N
#   manifest=ok|fail               manifest_out=LINE...  (sha256sum -c)
#   file=DEST MODE USER:GROUP SHA256   each file as installed, in the
#                                  order above ("file=DEST -": not there)
#   install_rc=N                   the worst exit status of install(1)
#   daemon_reload_rc=N             daemon_reload_out=LINE...
#   enable_rc=N                    enable_out=LINE...
#   enable_scd_rc=N                enable_scd_out=LINE...
#   update_grub_rc=N               update_grub_err=LINE...  (its stderr)
#   grub_script_check_rc=N         grub_script_check_out=LINE...
#   verify_rc=N                    verify_out=LINE...
#   done=1                         the last line: it ran to the end
#
# With smartconfig.deb in DIR (lab/e2e.py --deb, M5; it must be in
# MANIFEST too): only the lab's own 41_sclab and 43_sclab go in by
# install(1); the package goes in by dpkg -i (its postinst enables and
# starts the units and runs update-grub), and file= lines are the
# package's files. Then the keys are deb_rc and deb_out (dpkg's, and its
# scripts', output: postinst's update-grub says what it added there),
# enable_rc (the worst of systemctl is-enabled, one unit at a time) and
# enable_scd_rc (systemctl is-active scd); no daemon_reload, and no
# update-grub of its own: grub_script_check reads the grub.cfg that
# postinst's wrote.
#
# Exit status 0 when it ran to the end, whatever the steps gave; 2 when it
# could not start (not root, or a file not in MANIFEST or not matching
# it): then nothing is installed and the last line is manifest=fail or
# error=.
#
# SCLAB_TEST (cmd/sc/unit_test.go only): a directory whose bin/ is first
# in PATH and whose root/ stands for /. sudo drops it from the environment.

cd "$(dirname "$0")" || exit 2
R=
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
if [ -n "${SCLAB_TEST-}" ]; then
	R=$SCLAB_TEST/root
	PATH=$SCLAB_TEST/bin:/usr/bin:/bin
fi
LC_ALL=C SYSTEMD_PAGER= SYSTEMD_COLORS=0
export PATH LC_ALL SYSTEMD_PAGER SYSTEMD_COLORS

# What goes where: repo name, destination, mode.
files='sc /usr/sbin/sc 0755
42_smartconfig /etc/grub.d/42_smartconfig 0755
41_sclab /etc/grub.d/41_sclab 0755
43_sclab /etc/grub.d/43_sclab 0755
scd.service /etc/systemd/system/scd.service 0644
sc-boot-seen.service /etc/systemd/system/sc-boot-seen.service 0644
sc-boot-ok.service /etc/systemd/system/sc-boot-ok.service 0644
smartconfig-rescue.conf /etc/systemd/system/rescue.service.d/50-smartconfig.conf 0644
smartconfig-rescue.conf /etc/systemd/system/emergency.service.d/50-smartconfig.conf 0644'

# The package's files, where it puts them (the deb mode).
debfiles='/usr/sbin/sc
/etc/grub.d/42_smartconfig
/usr/lib/systemd/system/scd.service
/usr/lib/systemd/system/sc-boot-seen.service
/usr/lib/systemd/system/sc-boot-ok.service
/usr/lib/systemd/system/rescue.service.d/50-smartconfig.conf
/usr/lib/systemd/system/emergency.service.d/50-smartconfig.conf'
deb=
if [ -f smartconfig.deb ]; then
	deb=1
	files='41_sclab /etc/grub.d/41_sclab 0755
43_sclab /etc/grub.d/43_sclab 0755'
fi

# fileline DEST: file=DEST MODE USER:GROUP SHA256, or file=DEST -.
fileline() {
	if [ -f "$R$1" ]; then
		printf 'file=%s %s %s\n' "$1" "$(stat -c '%a %U:%G' "$R$1")" "$(sha256sum <"$R$1" | cut -d' ' -f1)"
	else
		printf 'file=%s -\n' "$1"
	fi
}

# lines KEY TEXT: each line of TEXT as KEY=LINE; nothing for no text.
lines() {
	[ -n "$2" ] || return 0
	printf '%s\n' "$2" | while IFS= read -r l; do
		printf '%s=%s\n' "$1" "$l"
	done
}

# step KEY COMMAND...: run it, then KEY_rc= and its output as KEY_out=.
step() {
	k=$1
	shift
	out=$("$@" </dev/null 2>&1)
	printf '%s_rc=%s\n' "$k" "$?"
	lines "${k}_out" "$out"
}

uid=$(id -u)
echo "uid=$uid"
if [ "$uid" != 0 ] && [ -z "$R" ]; then
	echo "error=not root"
	exit 2
fi

# Every file it installs must be in MANIFEST, and all of MANIFEST match.
out=$(sha256sum --strict -c MANIFEST 2>&1)
rc=$?
need=$files
[ -z "$deb" ] || need="$files
smartconfig.deb - -"
while read -r name dest mode; do
	re=$(printf '%s' "$name" | sed 's/\./\\./g')
	if ! grep -q "^[0-9a-f]\{64\} [ *]$re\$" MANIFEST 2>/dev/null; then
		out="$out
$name: not in MANIFEST"
		rc=1
	fi
done <<EOF
$need
EOF
if [ "$rc" != 0 ]; then
	lines manifest_out "$out"
	echo manifest=fail
	exit 2
fi
echo manifest=ok
lines manifest_out "$out"

worst=0
while read -r name dest mode; do
	install -D -o root -g root -m "$mode" "$name" "$R$dest" </dev/null
	rc=$?
	[ "$rc" -le "$worst" ] || worst=$rc
	fileline "$dest"
done <<EOF
$files
EOF
echo "install_rc=$worst"

if [ -n "$deb" ]; then
	step deb dpkg -i smartconfig.deb
	for dest in $debfiles; do fileline "$dest"; done
	# One unit at a time: is-enabled of several exits 0 if any one is.
	worst=0
	for u in sc-boot-seen.service sc-boot-ok.service scd.service; do
		out=$(systemctl is-enabled "$u" </dev/null 2>&1)
		rc=$?
		[ "$rc" -le "$worst" ] || worst=$rc
		lines enable_out "$out"
	done
	echo "enable_rc=$worst"
	step enable_scd systemctl is-active scd.service
else
	step daemon_reload systemctl daemon-reload
	step enable systemctl enable sc-boot-seen.service sc-boot-ok.service
	step enable_scd systemctl enable --now scd.service
	# update-grub writes grub.cfg itself; what it says (42_smartconfig's
	# "Adding SmartConfig rescue entry: ...") is on stderr.
	out=$(update-grub 2>&1 >/dev/null </dev/null)
	echo "update_grub_rc=$?"
	lines update_grub_err "$out"
fi
step grub_script_check grub-script-check "$R/boot/grub/grub.cfg"
# By name: the files as installed, with the drop-in in both services.
step verify systemd-analyze verify --man=no sc-boot-seen.service sc-boot-ok.service scd.service rescue.service emergency.service
echo done=1

#!/usr/bin/env bash
# Milestone 3 acceptance run (plan section 11, change C7). From the repo
# root, after `make build`:
#   sudo ./scripts/accept-m3.sh        (or: make accept-m3)
# Every checker gets fabricated content through `sc check --as`, as root
# and with the real validators: no fstab, sudoers, netplan or passwd file
# is ever written. The check lines of scd and sc edit are exercised on
# /etc/systemd/system/sc-accept.service, a unit that is never enabled, with
# bin/sc watch as a runtime unit (scd-accept) on a throwaway SC_HOME. An
# EXIT trap stops the unit and removes all of it. It refuses to run outside
# a VM (unless SC_ALLOW_REAL_HOST=1) or next to a real scd.
set -euo pipefail
umask 022

SC="$PWD/bin/sc"
UNIT=scd-accept
UNITFILE=/run/systemd/system/$UNIT.service
TESTUNIT=/etc/systemd/system/sc-accept.service

fail() { printf 'FAIL: %b\n' "$*" >&2; exit 1; }
step() { echo "== $*"; }
note() { echo "   note: $*"; }

# --- 0. Preconditions --------------------------------------------------
step "0. preconditions"
[ "$(id -u)" -eq 0 ] || fail "run as root: sudo $0"
if [ "${SC_ALLOW_REAL_HOST:-}" != 1 ] && ! systemd-detect-virt -q; then
	fail "not a virtual machine (set SC_ALLOW_REAL_HOST=1 to run anyway)"
fi
if systemctl is-active --quiet scd.service; then
	fail "a real scd.service is running (stop it for this run: systemctl stop scd; start it again after)"
fi
for c in /proc/[0-9]*/cmdline; do
	if tr '\0' ' ' <"$c" 2>/dev/null | grep -Eq '(^|/)sc watch( |$)'; then
		fail "a process runs sc watch: $(tr '\0' ' ' <"$c")"
	fi
done
[ -x "$SC" ] || fail "$SC missing, run: make build"
file "$SC" | grep -q 'statically linked' || fail "$SC is not statically linked"
for p in "$UNITFILE" "$TESTUNIT"; do
	[ ! -e "$p" ] && [ ! -L "$p" ] || fail "$p already exists"
done
# The real files the checkers read: they must be the same at the end.
REAL=(/etc/fstab /etc/sudoers /etc/ssh/sshd_config /etc/default/grub /boot/grub/grub.cfg
	/etc/nsswitch.conf /etc/hosts /etc/passwd /etc/group /etc/sysctl.conf)
sums() { local p; for p in "${REAL[@]}" /etc/netplan/*.yaml /etc/sudoers.d/*; do [ -e "$p" ] && sha256sum "$p"; done; }
SUMS=$(sums)

export SC_HOME
SC_HOME=$(mktemp -d /tmp/sc-accept3.XXXXXX)
chmod 700 "$SC_HOME"
WORK=$(mktemp -d /tmp/sc-accept3-work.XXXXXX)
cleanup() {
	systemctl stop "$UNIT" 2>/dev/null || true
	systemctl reset-failed "$UNIT" 2>/dev/null || true
	rm -f "$UNITFILE" "$TESTUNIT"
	systemctl daemon-reload || true
	rm -rf "$SC_HOME" "$WORK"
}
trap cleanup EXIT

# --- 1. Every checker, through --as ------------------------------------
step "1. sc check --as: fabricated content, real validators"
# as PATH WANT_EXIT RULE CONTENT: check CONTENT as if it were at PATH; the
# exit status must be WANT_EXIT and, when RULE is not "-", the output must
# name RULE.
as() {
	local path=$1 want=$2 rule=$3 content=$4 f out rc
	f="$WORK/$(basename "$path")-$RANDOM"
	printf '%b' "$content" >"$f"
	set +e
	out=$("$SC" check --as "$path" "$f" 2>&1)
	rc=$?
	set -e
	[ "$rc" -eq "$want" ] || fail "--as $path ($rule): exit $rc, want $want:\n$out"
	if [ "$rule" != - ]; then
		grep -q " $rule " <<<"$out" || fail "--as $path: no $rule in:\n$out"
	fi
	if grep -q '^note: .*no validator found' <<<"$out"; then
		note "$path: $(grep '^note: .*no validator found' <<<"$out" | head -1)"
	fi
}
U=$(findmnt -no UUID /)
as /etc/fstab 0 - "UUID=$U / ext4 defaults 0 1\n"
as /etc/fstab 2 fstab-source-missing "UUID=$U / ext4 defaults 0 1\nUUID=11111111-2222-3333-4444-555555555555 /data ext4 defaults 0 2\n"
as /etc/fstab 2 fstab-option-typo "UUID=$U / ext4 defaults 0 1\nUUID=$U /x ext4 defalts 0 2\n"
as /etc/sudoers.d/90-accept 0 - "root ALL=(ALL:ALL) ALL\n"
as /etc/sudoers.d/90-accept 2 sudoers-syntax "nobody ALL=(ALL NOPASSWD ALL\n"
as /etc/ssh/sshd_config.d/90-accept.conf 0 - "PasswordAuthentication no\n"
as /etc/ssh/sshd_config.d/90-accept.conf 2 sshd-invalid "PermitRootLogn no\n"
as $TESTUNIT 0 - "[Unit]\nDescription=accept\n[Service]\nExecStart=/bin/true\n"
as $TESTUNIT 2 unit-exec-missing "[Unit]\nDescription=accept\n[Service]\nExecStart=/usr/bin/sc-no-such-binary\n"
as $TESTUNIT 0 unit-unknown-key "[Unit]\nDescription=accept\n[Service]\nExecStart=/bin/true\nRestrt=always\n"
as /etc/default/grub 0 - 'GRUB_DEFAULT=0\nGRUB_CMDLINE_LINUX_DEFAULT="quiet splash"\n'
as /etc/default/grub 2 grub-default-syntax 'GRUB_DEFAULT=0\nGRUB_CMDLINE_LINUX_DEFAULT="quiet splash\n'
as /boot/grub/custom.cfg 0 - 'menuentry "accept" {\n  linux /vmlinuz\n}\n'
as /boot/grub/custom.cfg 2 grubcfg-syntax 'menuentry "accept" {\n  linux /vmlinuz\n'
as /etc/nsswitch.conf 0 - "passwd: files systemd\ngroup: files systemd\nhosts: files dns\n"
as /etc/nsswitch.conf 2 nsswitch-invalid "passwd: files systemd\nhosts: files [NOTFOUND=retrun] dns\n"
as /etc/nsswitch.conf 2 nsswitch-no-files "passwd: systemd\n"
as /etc/ld.so.preload 2 preload-missing-lib "/usr/lib/sc-no-such-lib.so\n"
as /etc/hosts 0 hosts-no-localhost "10.0.0.1 example\n"
as /etc/nologin 0 flag-nologin "maintenance\n"
# The second set (step 13).
as /etc/netplan/90-accept.yaml 0 - "network:\n  version: 2\n"
as /etc/netplan/90-accept.yaml 2 netplan-invalid "network:\n  version: 2\n  ethernets:\n    eth9:\n      dhcp4: maybe\n"
as /etc/passwd 2 passwd-root "root:x:1000:0:root:/root:/bin/bash\n"
# No root line: nss-systemd (passwd: files systemd, as on stock Ubuntu)
# supplies root, so only a warning.
if grep -Eq '^passwd:.*[[:space:]]systemd([[:space:]]|$)' /etc/nsswitch.conf; then
	as /etc/passwd 0 passwd-root "nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\n"
fi
as /etc/sysctl.d/90-accept.conf 0 - "vm.swappiness = 60\n"
as /etc/udev/rules.d/90-accept.rules 0 - 'SUBSYSTEM=="net", ACTION=="add", NAME="eth9"\n'
as /etc/udev/rules.d/90-accept.rules 2 udev-invalid 'SUBSYSTEM="net", ACTON=="add"\n'

# --- 2. The real system, read-only ---------------------------------------
step "2. sc check on this machine, read-only (findings are for the sign-off review)"
set +e
"$SC" check
rc=$?
set -e
[ $rc -ne 1 ] || fail "sc check could not check every file as root (exit 1)"
note "exit $rc (0: nothing, 2: findings above, to review)"

# --- 3. scd: a check after a recorded change -----------------------------
step "3. start $UNIT; a check line after a change, ok again after the fix"
sed -e "s|^ExecStart=.*|ExecStart=$SC watch|" \
	-e "s|^\[Service\]|[Service]\nEnvironment=SC_HOME=$SC_HOME|" scripts/scd.service >"$UNITFILE"
systemd-analyze verify "$UNITFILE" || fail "systemd-analyze verify"
systemctl daemon-reload
systemctl start "$UNIT"
INV=$(systemctl show -p InvocationID --value "$UNIT")
jr() { journalctl -q -o cat "_SYSTEMD_INVOCATION_ID=$INV" "$@"; }
wait_for() {
	local t=$1 what=$2
	shift 2
	local end=$((SECONDS + t))
	until "$@"; do
		[ $SECONDS -lt $end ] || fail "timed out after ${t}s waiting for $what"
		sleep 0.2
	done
}
in_journal() { jr | grep -q -- "$1"; }
wait_for 300 "the baseline" in_journal 'baseline: '
printf '[Unit]\nDescription=SmartConfig acceptance (never enabled)\n[Service]\nExecStart=/usr/bin/sc-no-such-binary\n' >"$TESTUNIT"
wait_for 60 "the check line" in_journal "$TESTUNIT: check: error unit-exec-missing, line 4: /usr/bin/sc-no-such-binary does not exist on this machine"
jr -p warning | grep -q "$TESTUNIT: check: error unit-exec-missing" || fail "the check line is not at warning priority"
printf '[Unit]\nDescription=SmartConfig acceptance (never enabled)\n[Service]\nExecStart=/bin/true\n' >"$TESTUNIT"
wait_for 60 "ok again" in_journal "$TESTUNIT: check: ok again"
if jr | grep -E ': check: ' | grep -v "$TESTUNIT" | grep -q .; then
	fail "check lines for other files: $(jr | grep -E ': check: ' | grep -v "$TESTUNIT" | head -3)"
fi

# --- 4. sc edit -----------------------------------------------------------
step "4. sc edit: quit, edit again, save anyway; the watcher adds no row"
ED=$WORK/editor
cat >"$ED" <<'EOF'
#!/bin/sh
# Run n writes a missing binary (1: quit, 2: edit again, 4: save anyway)
# or a clean /bin/false (3: what the second edit saves).
n=$(cat "$0.n" 2>/dev/null || echo 0); n=$((n + 1)); echo $n >"$0.n"
for f; do :; done
case $n in
1 | 2 | 4) bin=/usr/bin/sc-no-such-binary ;;
*) bin=/bin/false ;;
esac
printf '[Unit]\nDescription=SmartConfig acceptance (never enabled)\n[Service]\nExecStart=%s\n' "$bin" >"$f"
EOF
chmod 755 "$ED"
rows() { "$SC" log -n 0 "$TESTUNIT" | awk 'NR>1' | wc -l; }
before=$(rows)
# Quit at the prompt: the file is not touched.
set +e
out=$(printf 'q\n' | EDITOR=$ED SUDO_EDITOR= VISUAL= "$SC" edit "$TESTUNIT" 2>&1)
rc=$?
set -e
[ $rc -eq 2 ] && grep -q 'not saved' <<<"$out" || fail "quit: exit $rc:\n$out"
grep -q '/bin/true' "$TESTUNIT" || fail "quit changed the file"
# Edit again, then a clean version: saved. Piped answers are not echoed,
# so "saved" follows the prompt on its line.
set +e
out=$(printf 'e\n' | EDITOR=$ED SUDO_EDITOR= VISUAL= "$SC" edit "$TESTUNIT" 2>&1)
rc=$?
set -e
[ $rc -eq 0 ] && grep -q 'What now?' <<<"$out" && grep -q "\[e\]: saved $TESTUNIT as " <<<"$out" || fail "edit again: exit $rc:\n$out"
grep -q '/bin/false' "$TESTUNIT" || fail "edit again did not save"
# Save anyway: the row says so.
set +e
out=$(printf 's\n' | EDITOR=$ED SUDO_EDITOR= VISUAL= "$SC" edit "$TESTUNIT" 2>&1)
rc=$?
set -e
[ $rc -eq 0 ] || fail "save anyway: exit $rc:\n$out"
"$SC" log -n 1 "$TESTUNIT" | grep -q 'sc edit, saved with 1 error' || fail "save anyway: $("$SC" log -n 1 "$TESTUNIT")"
sleep 5 # the watcher's quiet period and more
after=$(rows)
[ $((after - before)) -eq 2 ] || fail "$((after - before)) rows for two saves, want 2:\n$("$SC" log -n 6 "$TESTUNIT")"
"$SC" log -n 2 "$TESTUNIT" | awk 'NR>1{print $4}' | grep -qv edit && fail "a row after the edits is not an edit row"
rm -f "$TESTUNIT"

# --- 5. sc scope ------------------------------------------------------------
step "5. sc scope"
out=$("$SC" scope /etc/fstab /etc/ld.so.cache /usr/bin/ls)
grep -q 'checker:  fstab' <<<"$out" || fail "sc scope /etc/fstab:\n$out"
grep -q 'exclude /etc/ld.so.cache' <<<"$out" || fail "sc scope /etc/ld.so.cache:\n$out"
grep -q 'outside every watched directory' <<<"$out" || fail "sc scope /usr/bin/ls:\n$out"

# --- 6. Nothing real changed -------------------------------------------------
step "6. the real files are as they were"
[ "$(sums)" = "$SUMS" ] || fail "a real file changed:\n$(diff <(echo "$SUMS") <(sums))"
[ ! -e "$TESTUNIT" ] || fail "$TESTUNIT left behind"

echo "PASS: M3 acceptance"

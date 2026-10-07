#!/bin/sh
# facts.sh: what the M4 lab's guest looks like, for lab/e2e.py; lab only,
# never shipped. It changes nothing: every command only reads, and the
# one that would write, sc restore, runs only on a read-only root, where
# it must be refused (check 3.8).
#
#   sh facts.sh normal               a normal boot: over ssh, with sudo
#   sh facts.sh rescue GOOD [BAD]    the rescue shell: root, on the console
#   sh facts.sh dump                 a failed run, whatever state it is in
#
# GOOD and BAD are snapshot ids: sc cat GOOD, sc restore GOOD, and sc diff
# GOOD BAD (without BAD: GOOD against /etc/fstab on disk).
#
# The output is blocks. Each starts with a line "== NAME rc=N": NAME has
# no space, N is the exit status of what printed the block (124: stopped
# at its time limit). The block's lines follow as printed, stdout and
# stderr together. The first block is "facts" (mode=, good=, bad=, uid=,
# tty=), the last "end". Which blocks each mode prints, and what is in
# them, is at "normal)", "rescue)" and "dump)" below.
#
# SCLAB_TEST (cmd/sc/unit_test.go only): a directory whose bin/ is first
# in PATH and whose root/ stands for / in the files read (not /proc, /sys
# and /dev). sudo drops it from the environment.

self=$(cd "$(dirname "$0")" && pwd)/$(basename "$0")
R=
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
if [ -n "${SCLAB_TEST-}" ]; then
	R=$SCLAB_TEST/root
	PATH=$SCLAB_TEST/bin:/usr/bin:/bin
fi
LC_ALL=C SYSTEMD_PAGER= SYSTEMD_COLORS=0 TERM=dumb
export PATH LC_ALL SYSTEMD_PAGER SYSTEMD_COLORS TERM

# The units SmartConfig installs, and the ones the verdicts rest on.
ours="sc-boot-seen.service sc-boot-ok.service scd.service"
states="local-fs.target multi-user.target emergency.target emergency.service rescue.target rescue.service $ours"

# The parts: each prints one block's lines; its exit status is the rc.

p_facts() {
	printf 'mode=%s\ngood=%s\nbad=%s\nuid=%s\ntty=%s\n' \
		"$SCLAB_MODE" "${SCLAB_GOOD-}" "${SCLAB_BAD-}" "$(id -u)" "$SCLAB_TTY"
}

# p_run COMMAND...: a command, as it is.
p_run() { "$@"; }

# p_cat FILE...: files of the kernel's, as they are.
p_cat() { cat "$@"; }

# p_file FILE: a file of the guest's (under SCLAB_TEST's root/).
p_file() { cat "$R$1"; }

# p_sha FILE: its sha256, or "-" when it is not a readable file.
p_sha() {
	if [ -f "$R$1" ] && [ -r "$R$1" ]; then
		sha256sum <"$R$1" | cut -d' ' -f1
	else
		echo -
		return 1
	fi
}

p_consoles() {
	cat /proc/consoles || return
	active=$(cat /sys/class/tty/console/active) || return
	printf 'active: %s\n' "$active"
}

# keep TEXT: TEXT with its line end back, nothing for nothing. A part
# that filters a command's output takes the output first, so its block
# has the command's exit status, not the filter's (the chunk D review,
# B14: a failing systemd-analyze gave an empty block with rc=0).
keep() { [ -z "$1" ] || printf '%s\n' "$1"; }

# p_mounts: "MOUNTPOINT SOURCE=".." LABEL=".." FSTYPE=".." OPTIONS=".."",
# or "MOUNTPOINT -" when nothing is mounted there.
p_mounts() {
	for m in / /boot /boot/efi /mnt/backup; do
		l=$(findmnt -n -P -o SOURCE,LABEL,FSTYPE,OPTIONS --mountpoint "$m" 2>/dev/null | tail -n 1)
		printf '%s %s\n' "$m" "${l:--}"
	done
}

# p_grub_defaults: the settings grub-mkconfig works with, sourced as it
# sources them: /etc/default/grub, then /etc/default/grub.d/*.cfg.
p_grub_defaults() {
	if [ -f "$R/etc/default/grub" ]; then
		. "$R/etc/default/grub"
	fi
	for x in "$R"/etc/default/grub.d/*.cfg; do
		if [ -e "$x" ]; then
			. "$x"
		fi
	done
	for v in GRUB_DEFAULT GRUB_TIMEOUT GRUB_TIMEOUT_STYLE GRUB_RECORDFAIL_TIMEOUT GRUB_TERMINAL \
		GRUB_CMDLINE_LINUX GRUB_CMDLINE_LINUX_DEFAULT GRUB_DISABLE_RECOVERY GRUB_DISABLE_LINUX_UUID; do
		eval "x=\${$v-}"
		printf '%s=%s\n' "$v" "$x"
	done
}

# p_paths: "PATH file MODE USER:GROUP SHA256", "PATH dir MODE USER:GROUP",
# "PATH link TARGET", "PATH other" or "PATH -" (not there).
p_paths() {
	for p in /usr/local/sbin/sc /etc/grub.d/41_sclab /etc/grub.d/42_smartconfig /etc/grub.d/43_sclab \
		/etc/systemd/system/scd.service /etc/systemd/system/sc-boot-seen.service \
		/etc/systemd/system/sc-boot-ok.service /etc/systemd/system/rescue.service.d/50-smartconfig.conf \
		/etc/systemd/system/emergency.service.d/50-smartconfig.conf /etc/default/grub.d/60-sclab.cfg \
		/etc/cloud/cloud-init.disabled /var/lib/smartconfig /var/lib/smartconfig/boots; do
		f=$R$p
		if [ -L "$f" ]; then
			printf '%s link %s\n' "$p" "$(readlink "$f")"
		elif [ -d "$f" ]; then
			printf '%s dir %s\n' "$p" "$(stat -c '%a %U:%G' "$f")"
		elif [ -f "$f" ]; then
			printf '%s file %s %s\n' "$p" "$(stat -c '%a %U:%G' "$f")" "$(p_sha "$p")"
		elif [ -e "$f" ]; then
			printf '%s other\n' "$p"
		else
			printf '%s -\n' "$p"
		fi
	done
}

# p_enabled, p_active UNIT...: "UNIT=STATE" each.
p_enabled() {
	for u in $ours; do
		printf '%s=%s\n' "$u" "$(systemctl is-enabled "$u" 2>&1)"
	done
}
p_active() {
	for u in "$@"; do
		printf '%s=%s\n' "$u" "$(systemctl is-active "$u" 2>&1)"
	done
}

# p_show UNIT: what checks 1.5 and 3.8 look at, as systemctl shows it.
p_show() {
	systemctl show -p Id -p LoadState -p UnitFileState -p ActiveState -p SubState -p Result \
		-p ExecMainCode -p ExecMainStatus -p ConditionResult -p ExecMainStartTimestampMonotonic \
		-p MainPID -p NRestarts "$1"
}

# p_dropins: rescue.service's and emergency.service's drop-ins.
p_dropins() { systemctl show -p Id -p DropInPaths rescue.service emergency.service; }

p_units_cat() { systemctl cat rescue.service emergency.service; }

p_failed() { systemctl --failed --no-legend --plain; }

# p_journal ID: this boot's journal lines of one syslog identifier.
p_journal() { journalctl -b -o cat --no-pager -n 500 -t "$1"; }

p_sc() { sc "$@"; }

p_analyze() { systemd-analyze "$@"; }

p_blame() { out=$(systemd-analyze blame) || return; keep "$out" | head -n 30; }

# p_hashes: "FILE SHA256" of what nothing in the rescue shell may change
# (check 3.8): the store, its journal files, the boots file, /etc/fstab.
p_hashes() {
	for p in /var/lib/smartconfig/changes.db /var/lib/smartconfig/changes.db-journal \
		/var/lib/smartconfig/changes.db-wal /var/lib/smartconfig/boots /etc/fstab; do
		printf '%s %s\n' "$p" "$(p_sha "$p")"
	done
	return 0
}

# p_sc_cat_sha ID: the sha256 of sc cat ID.
p_sc_cat_sha() {
	sc cat "$1" >/dev/null || return
	sc cat "$1" | sha256sum | cut -d' ' -f1
}

# p_sc_diff: GOOD against BAD, or against the file on disk.
p_sc_diff() {
	if [ -n "${SCLAB_BAD-}" ]; then
		sc diff "$SCLAB_GOOD" "$SCLAB_BAD"
	else
		sc diff "$SCLAB_GOOD"
	fi
}

# p_sc_check: sc check as the rescue shell gives it, with no TMPDIR.
p_sc_check() { env -u TMPDIR sc check /etc/fstab; }

# p_sc_restore: sc restore GOOD, which a read-only root must refuse. Never
# on a writable root, where it would restore (rc 125, "not run").
p_sc_restore() {
	case "$(findmnt -n -o OPTIONS --mountpoint / 2>/dev/null)" in
	ro | ro,*) ;;
	*)
		echo "not run: / is not mounted read-only"
		return 125
		;;
	esac
	sc restore "$SCLAB_GOOD"
}

# p_leftovers: sc's scratch directories left behind (none is right).
p_leftovers() {
	for d in /run /dev/shm /tmp; do
		for f in "$R$d"/sc-check-* "$R$d"/sc-store-*; do
			if [ -e "$f" ] || [ -L "$f" ]; then
				printf '%s\n' "${f#"$R"}"
			fi
		done
	done
	return 0
}

# p_vcs N: the text on virtual console N's screen, a line per row.
p_vcs() {
	if ! [ -r "/dev/vcs$1" ]; then
		echo "cannot read /dev/vcs$1"
		return 1
	fi
	cols=$(od -An -tu1 -j1 -N1 "/dev/vcsa$1" 2>/dev/null | tr -d ' ')
	out=$(tr '\000' ' ' <"/dev/vcs$1") || return
	printf '%s' "$out" | fold -w "${cols:-80}" | sed 's/ *$//'
}

p_procs() {
	out=$(ps -eo pid,stat,args) || return
	keep "$out" | grep -E 'sulogin|getty|journald|udevd|sshd|sc (watch|boot|status)' | grep -v grep
	return 0
}

p_targets() {
	out=$(systemctl list-units --type=target --state=active --no-legend --plain) || return
	keep "$out" | cut -d' ' -f1
}

p_jobs() { systemctl list-jobs --no-legend --plain; }

# p_grub_cfg_sclab: grub.cfg from 41_sclab's line to 43_sclab's.
p_grub_cfg_sclab() {
	sed -n '/^### BEGIN \/etc\/grub.d\/41_sclab ###$/,/^### END \/etc\/grub.d\/43_sclab ###$/p' "$R/boot/grub/grub.cfg"
}

# p_journal_tail N FILTER...: the last N lines of this boot's journal.
p_journal_tail() {
	n=$1
	shift
	journalctl -b -o short-monotonic --no-pager -n "$n" "$@"
}

p_dmesg() { out=$(dmesg) || return; keep "$out" | tail -n 40; }

# sec NAME PART [ARG...]: run p_PART in a shell of its own, with a time
# limit and no input, and print its block.
sec() {
	name=$1
	shift
	out=$(timeout -k 5 "$limit" sh "$self" --part "$@" </dev/null 2>&1)
	rc=$?
	printf '== %s rc=%s\n' "$name" "$rc"
	if [ -n "$out" ]; then
		printf '%s\n' "$out"
	fi
}

usage() {
	echo "usage: sh facts.sh normal | rescue GOOD [BAD] | dump" >&2
	exit 2
}

# A part, run by sec: the mode's variables come from the environment.
if [ "${1-}" = --part ]; then
	shift
	f=$1
	shift
	"p_$f" "$@"
	exit
fi

SCLAB_MODE=${1-}
SCLAB_GOOD=
SCLAB_BAD=
case "$SCLAB_MODE" in
normal | dump)
	[ $# = 1 ] || usage
	;;
rescue)
	[ $# = 2 ] || [ $# = 3 ] || usage
	SCLAB_GOOD=$2
	SCLAB_BAD=${3-}
	# Ids only: they go on sc's command line.
	for id in "$SCLAB_GOOD" "$SCLAB_BAD"; do
		case "$id" in *[!0-9a-fA-F]*) usage ;; esac
	done
	[ -n "$SCLAB_GOOD" ] || usage
	;;
*)
	usage
	;;
esac
SCLAB_TTY=$(tty 2>/dev/null)
export SCLAB_MODE SCLAB_GOOD SCLAB_BAD SCLAB_TTY

case "$SCLAB_MODE" in
normal)
	# Checks 0.3 (before the install), 0.6, 0.7, 1.x, 4.x and 5.1.
	limit=60
	sec facts facts
	sec boot-id cat /proc/sys/kernel/random/boot_id
	sec cmdline cat /proc/cmdline
	sec consoles consoles
	sec mounts mounts
	sec fstab file /etc/fstab
	sec fstab-sha256 sha /etc/fstab
	sec grubenv-stat run stat -c '%s %F' "$R/boot/grub/grubenv"
	sec grubenv run grub-editenv "$R/boot/grub/grubenv" list
	sec grub-defaults grub_defaults
	sec root-passwd run passwd -S root
	sec paths paths
	sec is-enabled enabled
	sec active active $states
	sec show:sc-boot-seen.service show sc-boot-seen.service
	sec show:sc-boot-ok.service show sc-boot-ok.service
	sec show:scd.service show scd.service
	sec dropins dropins
	sec units-cat units_cat
	sec system-running run systemctl is-system-running
	sec failed failed
	sec journal-sc-boot journal sc-boot
	sec journal-scd journal scd
	sec boots file /var/lib/smartconfig/boots
	sec sc-log-fstab sc log /etc/fstab
	sec sc-status sc status
	sec seen-before run systemctl show -p Before sc-boot-seen.service
	sec critical-chain analyze critical-chain multi-user.target
	sec analyze analyze
	sec blame blame
	sec grub-cfg file /boot/grub/grub.cfg
	;;
rescue)
	# Check 3.8, in the rescue shell. The hashes come first and last:
	# nothing in between may change them.
	limit=90
	sec facts facts
	sec boot-id cat /proc/sys/kernel/random/boot_id
	sec cmdline cat /proc/cmdline
	sec consoles consoles
	sec hashes-before hashes
	sec mounts mounts
	sec active active $states systemd-remount-fs.service
	sec show:sc-boot-seen.service show sc-boot-seen.service
	sec show:sc-boot-ok.service show sc-boot-ok.service
	sec boots file /var/lib/smartconfig/boots
	sec sc-log-fstab sc log /etc/fstab
	sec sc-status-console sc status --console
	sec sc-diff sc_diff
	sec sc-cat-good sc_cat_sha "$SCLAB_GOOD"
	sec sc-check sc_check
	sec sc-restore sc_restore
	sec hashes-after hashes
	sec leftovers leftovers
	sec vcs1 vcs 1
	;;
dump)
	# A failed run: the state of the boot, in whatever mode it is, kept
	# short enough for the serial console.
	limit=30
	sec facts facts
	sec boot-id cat /proc/sys/kernel/random/boot_id
	sec cmdline cat /proc/cmdline
	sec consoles consoles
	sec uptime cat /proc/uptime
	sec mounts mounts
	sec mounts-all run findmnt -rn -o TARGET,SOURCE,FSTYPE,OPTIONS
	sec system-running run systemctl is-system-running
	sec targets targets
	sec failed failed
	sec jobs jobs
	sec active active $states systemd-remount-fs.service systemd-journald.service systemd-udevd.service
	sec procs procs
	sec show:sc-boot-seen.service show sc-boot-seen.service
	sec show:sc-boot-ok.service show sc-boot-ok.service
	sec show:scd.service show scd.service
	sec grubenv run grub-editenv "$R/boot/grub/grubenv" list
	sec grub-cfg-sclab grub_cfg_sclab
	sec fstab file /etc/fstab
	sec boots file /var/lib/smartconfig/boots
	sec store run ls -la "$R/var/lib/smartconfig"
	sec sc-log-fstab sc log -n 10 /etc/fstab
	sec sc-status sc status
	sec journal-sc journal_tail 60 -t sc-boot -t scd
	sec journal-warn journal_tail 60 -p warning
	sec dmesg dmesg
	;;
esac
printf '== end rc=0\n'

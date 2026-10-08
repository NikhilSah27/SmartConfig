#!/usr/bin/env bash
# Milestone 4 acceptance run, the parts that need no reboot (plan section 6
# step 12, sign-off S1). From the repo root, after `make build`:
#   sudo ./scripts/accept-m4.sh        (or: make accept-m4)
# bin/sc on a throwaway SC_HOME, as root, on this machine's real systemd,
# GRUB and filesystems. No real file is written; systemd gets two runtime
# units and daemon-reloads, all undone:
# - the two boot units run as runtime units (sc-accept-boot-*) whose
#   /boot/grub/grubenv is a copy, bound over the real one (BindPaths=);
# - a broken /etc/fstab, a fake systemctl, the grub.d with 42_smartconfig
#   in it, and this machine's store remounted read-only exist only in
#   private mount namespaces (unshare), bound over the real paths;
# - the read-only test store is a tmpfs remounted read-only, and the files
#   chattr marks are under the work directory.
# An EXIT trap removes all of it, and says what it could not. It refuses to
# run outside a VM (unless SC_ALLOW_REAL_HOST=1). The boot itself (the
# menu, the rescue entry, the drop-in's report above the prompt) is make
# lab-e2e's, and S2 and S3's.
set -euo pipefail
umask 022

SC="$PWD/bin/sc"
SEEN=sc-accept-boot-seen
OK=sc-accept-boot-ok
UNITDIR=/run/systemd/system
GRUBENV=/boot/grub/grubenv
STORE=/var/lib/smartconfig

fail() { printf 'FAIL: %b\n' "$*" >&2; exit 1; }
step() { echo "== $*"; }
note() { echo "   note: $*"; }
# run CMD...: run CMD; its output, stderr included, is in $out and its
# exit status in $rc.
run() {
	set +e
	out=$("$@" 2>&1)
	rc=$?
	set -e
}

# --- 0. Preconditions --------------------------------------------------
step "0. preconditions"
[ "$(id -u)" -eq 0 ] || fail "run as root: sudo $0"
if [ "${SC_ALLOW_REAL_HOST:-}" != 1 ] && ! systemd-detect-virt -q; then
	fail "not a virtual machine (set SC_ALLOW_REAL_HOST=1 to run anyway)"
fi
[ -x "$SC" ] || fail "$SC missing, run: make build"
file "$SC" | grep -q 'statically linked' || fail "$SC is not statically linked"
case $PWD in *[[:space:]]*) fail "the repo's path has a space, which the units cannot take: $PWD" ;; esac
for t in unshare grub-mkconfig grub-editenv grub-script-check python3 chattr systemd-analyze findmnt cmp; do
	command -v "$t" >/dev/null || fail "$t not found"
done
for u in $SEEN $OK; do
	[ ! -e "$UNITDIR/$u.service" ] && [ ! -L "$UNITDIR/$u.service" ] || fail "$UNITDIR/$u.service already exists"
done
[ -f "$GRUBENV" ] && [ ! -L "$GRUBENV" ] && [ "$(stat -c %s "$GRUBENV")" -eq 1024 ] ||
	fail "$GRUBENV is not GRUB's 1024-byte environment block: sc leaves the menu flag alone here"
[ -x /usr/bin/systemctl ] && [ ! -e /usr/sbin/systemctl ] || fail "systemctl is not only /usr/bin/systemctl: the fake in step 2 would not be found"
[ -d /etc/default/grub.d ] || fail "no /etc/default/grub.d to switch os-prober off in"
[ -d "$STORE" ] || fail "no store at $STORE"
# The real files this run reads, or stands in for: the same at the end.
sums() {
	local p
	for p in /etc/fstab /etc/default/grub /etc/default/grub.d/* /etc/grub.d/*; do
		if [ -e "$p" ]; then sha256sum "$p"; fi
	done
	find /boot/grub -maxdepth 1 -type f -exec sha256sum {} + | sort
}
SUMS=$(sums)
REALBOOTS=$(ls -l "$STORE/boots" 2>&1 || true)
# sc's private directories (a hot journal's copy, check's scratch): none
# may be left behind.
leftovers() { ls -d /run/sc-check-* /run/sc-store-* /dev/shm/sc-check-* /dev/shm/sc-store-* /tmp/sc-check-* /tmp/sc-store-* 2>/dev/null || true; }
LEFT=$(leftovers)

# On the root filesystem, as /etc is: chattr needs ext4's flags.
WORK=$(mktemp -d /var/tmp/sc-accept4.XXXXXX)
chmod 700 "$WORK"
export SC_HOME=$WORK/home
RO=$WORK/ro
A=$WORK/attr
F=$A/f.conf
cleanup() {
	trap '' HUP INT TERM # a second Ctrl-C must not cut the cleanup short
	set +e
	local u left=
	for u in $SEEN $OK; do
		systemctl stop "$u" 2>/dev/null
		systemctl reset-failed "$u" 2>/dev/null
		rm -f "$UNITDIR/$u.service"
	done
	systemctl daemon-reload
	mountpoint -q "$RO" && umount "$RO"
	[ -e "$F" ] && chattr -ia "$F"
	[ -d "$A" ] && chattr -ia "$A"
	mountpoint -q "$RO" || rm -rf --one-file-system "$WORK"
	for u in $SEEN $OK; do
		[ -e "$UNITDIR/$u.service" ] && left="$left $UNITDIR/$u.service"
	done
	mountpoint -q "$RO" && left="$left $RO(mounted)"
	[ -e "$WORK" ] && left="$left $WORK"
	[ -z "$left" ] || echo "WARN: left behind:$left" >&2
	[ "$(sums)" = "$SUMS" ] || printf 'WARN: a real file changed:\n%s\n' "$(diff <(echo "$SUMS") <(sums))" >&2
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
"$SC" init >/dev/null

# --- 1. A store with this machine's fstab --------------------------------
step "1. sc snapshot /etc/fstab into the throwaway store (read only)"
ID1=$("$SC" snapshot -q -m "accept-m4: as it is" /etc/fstab)
echo "   $ID1 /etc/fstab"

# --- 2. The boot units, under systemd ------------------------------------
step "2. sc-boot-seen and sc-boot-ok as runtime units: a seen line and the flag, then ok and no flag"
cp "$GRUBENV" "$WORK/grubenv"
grub-editenv "$WORK/grubenv" list | grep -q '^smartconfig_pending=' &&
	fail "the copy of $GRUBENV already has smartconfig_pending: $(grub-editenv "$WORK/grubenv" list)"
# The units run a copy of bin/sc in $WORK: the repo is under /home, which
# their ProtectHome=yes hides (exit 203/EXEC: the chunk H hardening).
UNIT_SC=$WORK/sc
install -m 0755 "$SC" "$UNIT_SC"
# unit SRC NAME CMD: scripts/SRC as the runtime unit NAME, running sc
# CMD from that copy on the throwaway store and the grubenv copy.
# RemainAfterExit= keeps it loaded after sc exits, with its invocation id
# and times: systemd unloads a finished oneshot nothing refers to.
unit() {
	local f=$UNITDIR/$2.service
	sed -e "s|/usr/sbin/sc|$UNIT_SC|g" \
		-e "s|^RequiresMountsFor=.*|RequiresMountsFor=$WORK|" \
		-e "s|sc-boot-seen\.service|$SEEN.service|g" \
		-e "s|^\[Service\]|[Service]\nEnvironment=SC_HOME=$SC_HOME\nBindPaths=$WORK/grubenv:$GRUBENV\nRemainAfterExit=yes|" \
		"scripts/$1" >"$f"
	# Without these lines sc would write the real grubenv and store.
	grep -qxF "Environment=SC_HOME=$SC_HOME" "$f" && grep -qxF "BindPaths=$WORK/grubenv:$GRUBENV" "$f" &&
		grep -qxF "ExecStart=$UNIT_SC $3" "$f" || fail "$f is not made as it should be:\n$(cat "$f")"
	systemd-analyze verify "$f" || fail "systemd-analyze verify $2"
}
unit sc-boot-seen.service $SEEN "boot seen"
unit sc-boot-ok.service $OK "boot verdict"
systemctl daemon-reload
BID=$(cat /proc/sys/kernel/random/boot_id)
# start NAME: start the oneshot unit NAME, which must succeed; its journal
# is in $out.
start() {
	systemctl start "$1" || fail "systemctl start $1:\n$(systemctl status --no-pager "$1" 2>&1 | tail -20)"
	[ "$(systemctl show -p Result --value "$1")" = success ] || fail "$1: $(systemctl show -p Result --value "$1")"
	local inv t0 t1
	inv=$(systemctl show -p InvocationID --value "$1")
	[ -n "$inv" ] || fail "$1 has no invocation id"
	journalctl --sync 2>/dev/null || true
	out=$(journalctl -q -o cat "_SYSTEMD_INVOCATION_ID=$inv")
	if [ "$1" = $OK ]; then # it says its verdict: wait for the line, 5 s at most
		local end=$((SECONDS + 5))
		while [ -z "$out" ] && [ $SECONDS -lt $end ]; do
			sleep 0.2
			out=$(journalctl -q -o cat "_SYSTEMD_INVOCATION_ID=$inv")
		done
	fi
	t0=$(systemctl show -p ExecMainStartTimestampMonotonic --value "$1")
	t1=$(systemctl show -p ExecMainExitTimestampMonotonic --value "$1")
	[ "$t0" -gt 0 ] && [ "$t1" -ge "$t0" ] || fail "$1: no run times ($t0, $t1)"
	note "$1 took $(((t1 - t0) / 1000)) ms"
}
start $SEEN
grep -qx "$BID seen [0-9]*" "$SC_HOME/boots" || fail "no seen line for boot $BID:\n$(cat "$SC_HOME/boots")"
grub-editenv "$WORK/grubenv" list | grep -qx 'smartconfig_pending=1' || fail "sc boot seen did not set the flag:\n$out"
[ -z "$out" ] || fail "sc boot seen said:\n$out"
start $OK
verdict=$(awk -v b="$BID" '$1 == b && ($2 == "ok" || $2 == "bad")' "$SC_HOME/boots")
[ "$(grep -c . <<<"$verdict")" -eq 1 ] || fail "not one verdict for boot $BID:\n$(cat "$SC_HOME/boots")"
read -r _ v _ row why <<<"$verdict"
echo "   verdict: $v, row $row, $why"
[ "$v" = ok ] || fail "this boot's verdict is $v ($why): after S2, every boot of this machine would show the menu"
[ "$row" -ge 1 ] || fail "the verdict's row is $row, not the store's newest"
grep -q "^boot $BID: ok (local-fs=active emergency=inactive rescue=inactive failed-units=" <<<"$out" ||
	fail "sc boot verdict said:\n$out"
# Set, then unset: the copy is the real block again, byte for byte.
cmp -s "$WORK/grubenv" "$GRUBENV" || fail "the ok verdict did not leave the grubenv copy as it was:\n$(grub-editenv "$WORK/grubenv" list)"
systemctl stop $SEEN $OK
for u in $SEEN $OK; do
	rm -f "$UNITDIR/$u.service"
done
systemctl daemon-reload

step "2b. a bad verdict (systemctl faked: local-fs.target inactive) keeps the flag set"
BAD=$WORK/bad
mkdir "$BAD"
SC_HOME=$BAD/home "$SC" init >/dev/null
cp "$GRUBENV" "$BAD/grubenv"
grub-editenv "$BAD/grubenv" set smartconfig_pending=1
cat >"$BAD/systemctl" <<'EOF'
#!/bin/sh
# A boot whose /data never mounted, as sc boot verdict asks about it.
case $1 in
is-active)
	shift
	for u; do echo inactive; done
	exit 3
	;;
list-units) exit 0 ;;
esac
exit 1
EOF
chmod 755 "$BAD/systemctl"
cp "$BAD/grubenv" "$BAD/grubenv.before"
run env SC_HOME="$BAD/home" unshare --mount --propagation private \
	sh -c 'mount --bind "$1" /usr/bin/systemctl && mount --bind "$2" '"$GRUBENV"' && exec "$3" boot verdict' sh "$BAD/systemctl" "$BAD/grubenv" "$SC"
[ "$rc" -eq 0 ] && grep -qx "boot $BID: bad (local-fs=inactive emergency=inactive rescue=inactive failed-units=0)" <<<"$out" ||
	fail "sc boot verdict with local-fs.target inactive: exit $rc:\n$out"
cmp -s "$BAD/grubenv" "$BAD/grubenv.before" || fail "the bad verdict changed grubenv:\n$(grub-editenv "$BAD/grubenv" list)"
run env SC_HOME="$BAD/home" "$SC" status
[ "$rc" -eq 2 ] && grep -q '^Failed since:  1 boot, last .*: a mount failed$' <<<"$out" || fail "status after a bad verdict: exit $rc:\n$out"

# --- 3. sc status --------------------------------------------------------
step "3. sc status: healthy, then a disk that is not there in /etc/fstab"
run "$SC" status
[ "$rc" -eq 0 ] || fail "status after an ok verdict: exit $rc:\n$out"
grep -q '^Last healthy:  this boot, ' <<<"$out" && grep -qx 'Nothing recorded has changed since this boot came up.' <<<"$out" ||
	fail "status after an ok verdict:\n$out"
# The broken version exists only in a private mount namespace, bound over
# /etc/fstab: the store records it under the real path.
{
	cat /etc/fstab
	echo "UUID=11111111-2222-3333-4444-555555555555 /data ext4 defaults 0 2"
} >"$WORK/fstab.bad"
chmod 644 "$WORK/fstab.bad"
ID2=$(unshare --mount --propagation private sh -c 'mount --bind "$1" /etc/fstab && exec "$2" snapshot -q -m "accept-m4: a disk that is not there" /etc/fstab' \
	sh "$WORK/fstab.bad" "$SC")
echo "   $ID2 /etc/fstab (made up)"
[ "$(sums)" = "$SUMS" ] || fail "a real file changed:\n$(diff <(echo "$SUMS") <(sums))"
run "$SC" status
echo "$out" | sed 's/^/   | /'
[ "$rc" -eq 2 ] || fail "status after the broken fstab: exit $rc, want 2"
grep -Eq "^$ID2 .*/etc/fstab +blocker fstab-source-missing" <<<"$out" || fail "status: no blocker line for $ID2"
grep -qx 'To put /etc/fstab back as it was during the last healthy boot:' <<<"$out" &&
	grep -qx "  sc restore $ID1" <<<"$out" || fail "status: no undo with sc restore $ID1"
# The rescue form; written to a pipe, it goes to the pipe only, not to
# this machine's consoles.
run "$SC" status --console
echo "$out" | sed 's/^/   | /'
[ "$rc" -eq 2 ] || fail "status --console: exit $rc, want 2"
grep -Eq "^$ID2 .*blocker fstab-source-missing.* /etc/fstab$" <<<"$out" && grep -qx "  sc restore $ID1" <<<"$out" ||
	fail "status --console: no blocker line for $ID2 or no sc restore $ID1"
wide=$(awk 'length > 80' <<<"$out")
[ -z "$wide" ] || fail "status --console: lines over 80 columns:\n$wide"
[ "$(wc -l <<<"$out")" -le 20 ] || fail "status --console: $(wc -l <<<"$out") lines, over the 20 the console has for it"

# --- 4. sc restore and chattr ---------------------------------------------
step "4. sc restore refuses an immutable or append-only file or directory, and writes nothing"
mkdir -m 755 "$A"
printf 'one\n' >"$F"
F1=$("$SC" snapshot -q "$F")
printf 'two\n' >"$F"
F2=$("$SC" snapshot -q "$F")
rows() { "$SC" log -n 0 "$F" | wc -l; }
# refuse FLAGS ON WANT: with chattr FLAGS on ON, sc restore F1 must exit
# 1 saying WANT, and change neither the file nor the store.
refuse() {
	local n
	n=$(rows)
	chattr "$1" "$2" || fail "chattr $1 $2"
	run "$SC" restore "$F1"
	chattr "-${1#+}" "$2"
	[ "$rc" -eq 1 ] && grep -qF "$3" <<<"$out" || fail "restore with $1 on $2: exit $rc:\n$out"
	[ "$(cat "$F")" = two ] || fail "restore with $1 on $2 changed the file"
	[ "$(rows)" -eq "$n" ] || fail "restore with $1 on $2 added a row"
}
refuse +i "$F" "$F is immutable (chattr +i), so $F cannot be replaced: run chattr -i $F first"
refuse +a "$F" "$F is append-only (chattr +a), so $F cannot be replaced: run chattr -a $F first"
refuse +i "$A" "$A is immutable (chattr +i), so $F cannot be replaced: run chattr -i $A first"
refuse +a "$A" "$A is append-only (chattr +a), so $F cannot be replaced: run chattr -a $A first"
run "$SC" restore "$F1"
[ "$rc" -eq 0 ] && [ "$(cat "$F")" = one ] || fail "restore without the flags: exit $rc:\n$out"

# --- 5, 6. A read-only store, and one with a hot journal -------------------
step "5. a read-only store (a tmpfs remounted read-only), and a copy with a hot journal"
ROWS=$("$SC" log -n 0)
mkdir "$RO"
mount -t tmpfs -o size=256m,mode=0700 sc-accept4 "$RO"
cp -a "$SC_HOME" "$RO/home"
cp -a "$SC_HOME" "$RO/hot"
# A writer killed in the middle of a transaction, after SQLite has written
# some of it to the database file: the rollback journal it leaves is hot.
cat >"$WORK/hot.py" <<'EOF'
import os, sqlite3, sys
db = sys.argv[1]
before = os.path.getsize(db)
c = sqlite3.connect(db, isolation_level=None)
c.execute("PRAGMA cache_size=10")
c.execute("BEGIN IMMEDIATE")
c.execute("CREATE TABLE sc_accept_hot(x BLOB)")
for _ in range(400):
    c.execute("INSERT INTO sc_accept_hot VALUES (randomblob(4000))")
j = db + "-journal"
if os.path.getsize(db) <= before or not os.path.exists(j) or os.path.getsize(j) == 0:
    sys.exit("no hot journal: database %d bytes, was %d" % (os.path.getsize(db), before))
os._exit(0)
EOF
python3 -I "$WORK/hot.py" "$RO/hot/changes.db" || fail "could not make a hot journal"
mount -o remount,ro "$RO"
FSTAB=$(sha256sum </etc/fstab)
# reads HOME: what the rescue shell runs, on the store in HOME.
reads() {
	export SC_HOME=$1
	run "$SC" log -n 0
	[ "$rc" -eq 0 ] || fail "$1: sc log: exit $rc:\n$out"
	[ "$(grep -v '^sc: note: ' <<<"$out")" = "$ROWS" ] || fail "$1: sc log is not the store's rows:\n$out"
	[ "$("$SC" cat "$ID1" 2>/dev/null | sha256sum)" = "$FSTAB" ] || fail "$1: sc cat $ID1 is not /etc/fstab"
	run "$SC" diff "$ID1" "$ID2"
	grep -q '^+UUID=11111111-2222-3333-4444-555555555555 /data ' <<<"$out" || fail "$1: sc diff $ID1 $ID2:\n$out"
	run "$SC" status
	[ "$rc" -eq 2 ] && grep -Eq "^$ID2 .*/etc/fstab +blocker fstab-source-missing" <<<"$out" || fail "$1: sc status: exit $rc:\n$out"
	run "$SC" status --console
	[ "$rc" -eq 2 ] && grep -qx "  sc restore $ID1" <<<"$out" || fail "$1: sc status --console: exit $rc:\n$out"
	# The validators' scratch copies cannot go in the store's tmp/ here;
	# as root they go to /run (TempParents), and are gone after.
	run "$SC" check --as /etc/fstab "$WORK/fstab.bad"
	[ "$rc" -eq 2 ] && grep -q ' fstab-source-missing ' <<<"$out" || fail "$1: sc check --as: exit $rc:\n$out"
	! grep -q 'no validator' <<<"$out" || fail "$1: sc check ran no validator:\n$out"
	run "$SC" restore "$F2"
	[ "$rc" -eq 1 ] && grep -q 'read-only' <<<"$out" || fail "$1: sc restore $F2: exit $rc:\n$out"
	[ "$(cat "$F")" = one ] || fail "$1: sc restore $F2 changed $F"
	export SC_HOME=$WORK/home
}
reads "$RO/home"
step "6. the store with a hot journal, read through a repaired copy"
reads "$RO/hot"
SC_HOME=$RO/hot "$SC" log -n 1 2>&1 >/dev/null | grep -qx 'sc: note: the store has a write a crash left unfinished; sc reads a repaired copy, and the store itself is repaired by the next sc run on a writable root' ||
	fail "no note about the unfinished write"
[ "$(leftovers)" = "$LEFT" ] || fail "sc left private directories behind:\n$(diff <(echo "$LEFT") <(leftovers))"
umount "$RO"

# --- 7. 42_smartconfig -------------------------------------------------------
step "7. 42_smartconfig under grub-mkconfig, with this machine's /boot and devices"
# grub-mkconfig twice, each in a private mount namespace where /etc/grub.d
# is a copy, with and without 42_smartconfig; it writes only the -o file,
# after its own grub-script-check. grub-mkconfig sets
# GRUB_DISABLE_OS_PROBER itself and then reads /etc/default/grub.d, so a
# copy of that with one more file keeps os-prober off in both.
cp -a /etc/grub.d "$WORK/grub.d-stock"
rm -f "$WORK/grub.d-stock/42_smartconfig"
cp -a "$WORK/grub.d-stock" "$WORK/grub.d-sc"
install -m 0755 scripts/42_smartconfig "$WORK/grub.d-sc/"
cp -a /etc/default/grub.d "$WORK/default.grub.d"
echo GRUB_DISABLE_OS_PROBER=true >"$WORK/default.grub.d/99-sc-accept.cfg"
mkconfig() {
	unshare --mount --propagation private \
		sh -c 'mount --bind "$1" /etc/grub.d && mount --bind "$3" /etc/default/grub.d && exec grub-mkconfig -o "$2"' \
		sh "$1" "$2" "$WORK/default.grub.d" 2>"$2.err" || fail "grub-mkconfig with $1:\n$(cat "$2.err")"
}
mkconfig "$WORK/grub.d-stock" "$WORK/stock.cfg"
mkconfig "$WORK/grub.d-sc" "$WORK/sc.cfg"
grep '42_smartconfig\|SmartConfig' "$WORK/sc.cfg.err" | sed 's/^/   | /' || true
B='^### BEGIN /etc/grub.d/42_smartconfig ###$'
E='^### END /etc/grub.d/42_smartconfig ###$'
part=$(sed -n "\|$B|,\|$E|p" "$WORK/sc.cfg")
echo "$part" | sed 's/^/   | /'
d=$(diff -B "$WORK/stock.cfg" <(sed "\|$B|,\|$E|d" "$WORK/sc.cfg") || true)
[ -z "$d" ] || fail "42_smartconfig changed more than its own part:\n$d"
# The rescue entry boots what the default entry boots, with root= as it
# has it, and the recipe instead of "quiet splash".
klinux=$(awk '$1 == "linux" {print; exit}' "$WORK/stock.cfg")
kinitrd=$(awk '$1 == "initrd" {print; exit}' "$WORK/stock.cfg")
[ -n "$klinux" ] && [ -n "$kinitrd" ] || fail "no linux or initrd line in the default entry"
slinux=$(awk '$1 == "linux"' <<<"$part")
sinitrd=$(awk '$1 == "initrd"' <<<"$part")
[ -n "$slinux" ] && [ -n "$sinitrd" ] || fail "no linux or initrd line in the rescue entry"
grep -qx "menuentry 'SmartConfig rescue' --class ubuntu --class gnu-linux --class os --id smartconfig-rescue {" <<<"$part" ||
	fail "no SmartConfig rescue entry"
[ "$(awk '{print $2}' <<<"$slinux")" = "$(awk '{print $2}' <<<"$klinux")" ] || fail "the rescue kernel is not the default's:\n$slinux\n$klinux"
root=$(tr ' \t' '\n\n' <<<"$klinux" | grep '^root=')
tr ' \t' '\n\n' <<<"$slinux" | grep -qxF -- "$root" || fail "the rescue entry's root= is not the default's ($root):\n$slinux"
[[ $slinux == *" ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1" ]] || fail "the rescue entry lacks the recipe:\n$slinux"
! grep -Eqw 'quiet|splash' <<<"$slinux" || fail "the rescue entry keeps quiet splash:\n$slinux"
[ "$(awk '{$1 = ""; print}' <<<"$sinitrd")" = "$(awk '{$1 = ""; print}' <<<"$kinitrd")" ] ||
	fail "the rescue initrd is not the default's:\n$sinitrd\n$kinitrd"
grep -qxF $'\techo\t'"'SmartConfig rescue: root read-only, /etc/fstab ignored'" <<<"$part" || fail "no echo line"
grep -qxF 'if [ "${smartconfig_pending}" = "1" ] ; then' <<<"$part" || fail "no menu flag block"
grub-script-check "$WORK/sc.cfg" || fail "grub-script-check"
[ "$(uname -r)" = "$(awk '{sub(".*/vmlinuz-", "", $2); print $2}' <<<"$slinux")" ] ||
	note "the rescue entry's kernel is not the running one ($(uname -r)): a newer one boots next"
if cmp -s /boot/grub/grub.cfg "$WORK/stock.cfg"; then
	note "this machine's grub.cfg is what grub-mkconfig makes today: S2's update-grub adds only the part above"
else
	note "this machine's grub.cfg is not what grub-mkconfig makes today: S2's update-grub changes more than the part above"
fi

# --- 8. This machine's store ---------------------------------------------------
step "8. sc status on this machine's store, remounted read-only as in the rescue shell (for the sign-off review)"
# realstore CMD...: sc CMD on the real store, bound read-only over itself
# in a private mount namespace: nothing in it can be written.
realstore() {
	run unshare --mount --propagation private \
		sh -c 'mount --bind "$1" "$1" && mount -o remount,bind,ro "$1" && shift && exec env -u SC_HOME "$@"' sh "$STORE" "$SC" "$@"
}
realstore status
echo "$out" | sed 's/^/   | /'
[ "$rc" -ne 1 ] || fail "sc status could not read this machine's store read-only (exit 1)"
note "exit $rc (0: healthy; 2: a failed boot or a problem above, to review; no verdicts before S2)"
realstore status --console
[ "$rc" -ne 1 ] || fail "sc status --console on this machine's store (exit 1):\n$out"
wide=$(awk 'length > 80' <<<"$out")
[ -z "$wide" ] && [ "$(wc -l <<<"$out")" -le 20 ] || fail "sc status --console on this machine's store does not fit 80x20:\n$out"

# --- 9. Nothing real changed -----------------------------------------------------
step "9. the real files are as they were"
[ "$(sums)" = "$SUMS" ] || fail "a real file changed:\n$(diff <(echo "$SUMS") <(sums))"
[ "$(ls -l "$STORE/boots" 2>&1 || true)" = "$REALBOOTS" ] || fail "$STORE/boots changed"
[ "$(leftovers)" = "$LEFT" ] || fail "sc left private directories behind:\n$(diff <(echo "$LEFT") <(leftovers))"
for u in $SEEN $OK; do
	[ ! -e "$UNITDIR/$u.service" ] || fail "$UNITDIR/$u.service left behind"
done

echo "PASS: M4 acceptance (no reboot)"

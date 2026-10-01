#!/usr/bin/env bash
# Milestone 2 acceptance run (plan section 12). From the repo root, after
# `make build`:
#   sudo ./scripts/accept-m2.sh        (or: make accept-m2)
# It runs bin/sc watch as a runtime systemd unit (scd-accept) with a
# throwaway SC_HOME, and changes only its own test paths: /etc/sc-accept.d,
# /etc/sc-accept2.d and /etc/modprobe.d/sc-accept-inert.txt (modprobe reads
# only *.conf). An EXIT trap stops the unit and removes all of it. It
# refuses to run outside a VM (unless SC_ALLOW_REAL_HOST=1) or next to a
# real scd.
set -euo pipefail

SC="$PWD/bin/sc"
D=/etc/sc-accept.d
D2=/etc/sc-accept2.d
INERT=/etc/modprobe.d/sc-accept-inert.txt
UNIT=scd-accept
UNITFILE=/run/systemd/system/$UNIT.service

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo "== $*"; }
note() { echo "   note: $*"; }

# --- 0. Preconditions --------------------------------------------------
step "0. preconditions"
[ "$(id -u)" -eq 0 ] || fail "run as root: sudo $0"
if [ "${SC_ALLOW_REAL_HOST:-}" != 1 ] && ! systemd-detect-virt -q; then
	fail "not a virtual machine (set SC_ALLOW_REAL_HOST=1 to run anyway)"
fi
if systemctl is-active --quiet scd.service; then fail "a real scd.service is running"; fi
for c in /proc/[0-9]*/cmdline; do
	if tr '\0' ' ' <"$c" 2>/dev/null | grep -Eq '(^|/)sc watch( |$)'; then
		fail "a process runs sc watch: $(tr '\0' ' ' <"$c")"
	fi
done
[ -x "$SC" ] || fail "$SC missing, run: make build"
file "$SC" | grep -q 'statically linked' || fail "$SC is not statically linked"
HAVE_PY=1
command -v python3 >/dev/null || { HAVE_PY=0; note "no python3: steps 3, 16 and the integrity checks are skipped"; }
for p in "$D" "$D2" "$INERT" "$UNITFILE"; do
	[ ! -e "$p" ] || fail "$p already exists"
done
sum() { sha256sum "$1" | cut -d' ' -f1; }
SUM_FSTAB=$(sum /etc/fstab)
SUM_HOSTS=$(sum /etc/hosts)
SUM_MID=$(sum /etc/machine-id)
T0=$(date +%s)

export SC_HOME
SC_HOME=$(mktemp -d /tmp/sc-accept.XXXXXX)
chmod 700 "$SC_HOME"
LISTING=$(mktemp /tmp/sc-accept-listing.XXXXXX)
cleanup() {
	systemctl stop "$UNIT" 2>/dev/null || true
	systemctl reset-failed "$UNIT" 2>/dev/null || true
	rm -f "$UNITFILE"
	systemctl daemon-reload || true
	rm -rf "$D" "$D2" "$INERT" "$LISTING" "$SC_HOME"
}
trap cleanup EXIT

# Helpers. Rows of sc log: ID DATE TIME ORIGIN FILE SIZE WHAT...
rows() { "$SC" log -n 0 "$1" | awk 'NR>1'; }
nrows() { rows "$1" | wc -l; }
newest() { rows "$1" | awk -v f="$2" 'NR==1{print $f}'; }
whats() { rows "$1" | awk '{w=""; for (i=7;i<=NF;i++) w=w (i>7?" ":"") $i; print w}' | tac | paste -sd'|'; }
inv() { systemctl show -p InvocationID --value "$UNIT"; }
jr() { journalctl -q -o cat "_SYSTEMD_INVOCATION_ID=$INV" "$@"; }
# wait_for SECONDS DESCRIPTION COMMAND...: poll until COMMAND succeeds.
wait_for() {
	local t=$1 what=$2
	shift 2
	local end=$((SECONDS + t))
	until "$@"; do
		[ $SECONDS -lt $end ] || fail "timed out after ${t}s waiting for $what"
		sleep 0.1
	done
}
has_rows() { [ "$(nrows "$1")" -ge "$2" ]; }
baseline_done() { jr | grep -q 'baseline: '; }
py_ro() { python3 - "$SC_HOME/changes.db" "$@"; }

# --- 1. Scope against the real /etc ------------------------------------
step "1. default scope against the real /etc"
find /etc /boot/grub -xdev -printf '%y %p\n' >"$LISTING" 2>/dev/null || true
chmod 644 "$LISTING"
if [ -n "${SUDO_USER:-}" ]; then
	sudo -u "$SUDO_USER" -H bash -lc "cd '$PWD' && SC_ETC_LISTING='$LISTING' go test -count=1 -run TestDefaultScopeRealListing ./internal/scope" ||
		fail "TestDefaultScopeRealListing"
else
	note "SUDO_USER unset: skipped"
fi

# --- 2. Start ----------------------------------------------------------
step "2. start $UNIT"
sed -e "s|^ExecStart=.*|ExecStart=$SC watch|" \
	-e "s|^\[Service\]|[Service]\nEnvironment=SC_HOME=$SC_HOME|" scripts/scd.service >"$UNITFILE"
systemd-analyze verify "$UNITFILE" || fail "systemd-analyze verify"
systemctl daemon-reload
systemctl start "$UNIT"
INV=$(inv)

# --- 3. Kill during the startup rescan ----------------------------------
step "3. kill -9 during the startup rescan"
if [ $HAVE_PY = 1 ]; then
	has_any_row() {
		py_ro <<'EOF' 2>/dev/null
import sqlite3, sys
c = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
sys.exit(0 if c.execute("select count(*) from changes").fetchone()[0] > 0 else 1)
EOF
	}
	wait_for 60 "a first row" has_any_row
	if baseline_done; then
		note "the baseline finished before the kill; TestKillDuringBaseline is the strict check"
	fi
	systemctl kill -s KILL "$UNIT"
	old=$INV
	new_inv() { INV=$(inv); [ -n "$INV" ] && [ "$INV" != "$old" ] && systemctl is-active --quiet "$UNIT"; }
	wait_for 20 "systemd to restart $UNIT" new_inv
else
	note "skipped (no python3)"
fi

# --- 4. Baseline -------------------------------------------------------
step "4. baseline"
start=$SECONDS
wait_for 300 "baseline: in this invocation" baseline_done
echo "   baseline took $((SECONDS - start))s after the (re)start"
[ "$(stat -c %a "$SC_HOME")" = 700 ] || fail "SC_HOME is $(stat -c %a "$SC_HOME")"
[ "$(stat -c %a "$SC_HOME/changes.db")" = 600 ] || fail "changes.db is $(stat -c %a "$SC_HOME/changes.db")"
if [ $HAVE_PY = 1 ]; then
	py_ro <<'EOF' || fail "integrity or duplicate first seen rows"
import sqlite3, sys
c = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
ok = c.execute("pragma integrity_check").fetchone()[0]
dup = c.execute("select path from changes where intent like 'first seen%' group by path having count(*) > 1").fetchall()
print("   integrity_check:", ok, "; paths with two first seen rows:", len(dup))
sys.exit(0 if ok == "ok" and not dup else 1)
EOF
fi
LOG=$("$SC" log -n 0)
for p in /etc/fstab /etc/sudoers /etc/default/grub /etc/pam.d/common-auth /boot/grub/grub.cfg; do
	grep -q " $p " <<<"$LOG" || fail "$p not recorded"
done
[ -e /root/.ssh/authorized_keys ] && { grep -q ' /root/.ssh/authorized_keys ' <<<"$LOG" || fail "/root/.ssh/authorized_keys not recorded"; }
[ -L /etc/systemd/system/display-manager.service ] && {
	[ "$(newest /etc/systemd/system/display-manager.service 6)" = link ] || fail "display-manager.service is not a link row"
}
for p in /etc/ssh/ssh_host_ed25519_key /etc/machine-id; do
	[ "$(newest $p 6)" = digest ] || fail "$p is not a digest row"
done
if awk 'NR>1{print $5}' <<<"$LOG" | grep -Eq '^/etc/ssl/certs/|^/boot/grub/grubenv$|^/etc/ld.so.cache$'; then
	fail "an excluded path was recorded"
fi

# --- 5. Refusals --------------------------------------------------------
step "5. refusals write nothing"
before=$("$SC" log -n 0 | wc -l)
out=$("$SC" watch 2>&1) && fail "a second sc watch started"
grep -q 'already running' <<<"$out" || fail "second watch: $out"
out=$(SC_HOME=$D/home "$SC" watch 2>&1) && fail "SC_HOME in /etc accepted"
grep -q 'inside watched root /etc' <<<"$out" || fail "SC_HOME in /etc: $out"
[ ! -e "$D" ] || fail "$D was created"
KEY=$(newest /etc/ssh/ssh_host_ed25519_key 1)
for c in cat diff restore; do
	out=$("$SC" $c "$KEY" 2>&1) && fail "sc $c of the host key succeeded"
	[ "$(wc -l <<<"$out")" -eq 1 ] || fail "sc $c of the host key: $out"
done
out=$("$SC" restore "$(newest /etc/machine-id 1)" 2>&1) && fail "machine-id restored"
grep -q 'fingerprint-only' <<<"$out" || fail "machine-id: $out"
[ "$(sum /etc/machine-id)" = "$SUM_MID" ] || fail "machine-id changed"
if [ -n "${SUDO_USER:-}" ]; then
	uhome=$(getent passwd "$SUDO_USER" | cut -d: -f6)
	if [ -n "$(rows "$uhome/.ssh/authorized_keys")" ]; then
		out=$("$SC" restore "$(newest "$uhome/.ssh/authorized_keys" 1)" 2>&1) && fail "a user's authorized_keys restored"
		[ "$(wc -l <<<"$out")" -eq 1 ] || fail "user authorized_keys: $out"
	fi
fi
[ "$("$SC" log -n 0 | wc -l)" = "$before" ] || fail "a refusal wrote a row"

# --- 6-11. Writers -------------------------------------------------------
step "6. new directory"
mkdir "$D" && printf 'one\n' >"$D/a.conf"
wait_for 3 "a.conf rows" has_rows "$D/a.conf" 2
[ "$(whats "$D/a.conf")" = "did not exist|created" ] || fail "a.conf: $(whats "$D/a.conf")"
A_ID=$(newest "$D/a.conf" 1)

step "7. sed -i"
sed -i s/one/two/ "$D/a.conf"
wait_for 5 "the sed row" has_rows "$D/a.conf" 3
sleep 1
[ "$(nrows "$D/a.conf")" = 3 ] && [ "$(newest "$D/a.conf" 7)" = changed ] || fail "sed: $(whats "$D/a.conf")"
[ "$("$SC" cat "$(newest "$D/a.conf" 1)")" = two ] || fail "sed content"
if "$SC" log -n 0 | awk '{print $5}' | grep -Eq '/sed[^/]{6}$'; then fail "a sed temp file was recorded"; fi

step "8. slow in-place write"
{ echo p1; sleep 0.2; echo p2; } >"$D/a.conf"
wait_for 5 "the slow write row" has_rows "$D/a.conf" 4
sleep 1
[ "$(nrows "$D/a.conf")" = 4 ] || fail "slow write: $(whats "$D/a.conf")"
[ "$("$SC" cat "$(newest "$D/a.conf" 1)")" = "$(printf 'p1\np2')" ] || fail "slow write content"

step "9. remove and recreate"
rm "$D/a.conf"
sleep 0.1
echo three >"$D/a.conf"
wait_for 5 "the recreate row" has_rows "$D/a.conf" 5
sleep 1
[ "$(nrows "$D/a.conf")" = 5 ] && [ "$(newest "$D/a.conf" 7)" = changed ] || fail "recreate: $(whats "$D/a.conf")"

step "10. metadata"
chmod 600 "$D/a.conf"
wait_for 5 "the mode row" has_rows "$D/a.conf" 6
[ "$(rows "$D/a.conf" | awk 'NR==1{print $7, $8}')" = "mode 0644->0600" ] || fail "chmod: $(whats "$D/a.conf")"
touch "$D/a.conf"
sleep 2
[ "$(nrows "$D/a.conf")" = 6 ] || fail "touch gave a row"

step "11. symlinks"
ln -s /dev/null "$D/x.service"
wait_for 3 "x.service rows" has_rows "$D/x.service" 2
[ "$(newest "$D/x.service" 6)" = link ] && [ "$(whats "$D/x.service")" = "did not exist|created" ] || fail "link: $(whats "$D/x.service")"
X_DNE=$(rows "$D/x.service" | awk 'NR==2{print $1}')
ln -sfn /etc/hosts "$D/x.service"
wait_for 5 "the second link row" has_rows "$D/x.service" 3
[ "$(newest "$D/x.service" 6)" = link ] || fail "ln -sfn: $(whats "$D/x.service")"

# --- 12-13. Restores ------------------------------------------------------
step "12. restore across processes, 5 times"
for i in 1 2 3 4 5; do
	"$SC" restore "$A_ID" >/dev/null
	sleep 2
	[ "$(rows "$D/a.conf" | awk 'NR<=2{print $4}' | paste -sd' ')" = "restore pre-restore" ] ||
		fail "round $i: $(rows "$D/a.conf" | head -3)"
	[ "$(cat "$D/a.conf")" = one ] && [ "$(stat -c %a "$D/a.conf")" = 644 ] || fail "round $i: content or mode"
done

step "13. undo a creation"
out=$("$SC" restore "$X_DNE")
grep -q '^removed ' <<<"$out" || fail "restore of did-not-exist: $out"
[ ! -e "$D/x.service" ] && [ ! -L "$D/x.service" ] || fail "x.service still there"
n=$(nrows "$D/x.service")
sleep 2
[ "$(newest "$D/x.service" 4)" = restore ] && [ "$(nrows "$D/x.service")" = "$n" ] || fail "an auto row after the restore"
out=$("$SC" restore "$X_DNE")
grep -q '^nothing to do' <<<"$out" || fail "second restore: $out"
[ "$(nrows "$D/x.service")" = "$n" ] || fail "nothing to do wrote a row"

# --- 14. Delete and move ------------------------------------------------
step "14. delete and move"
printf 'b\n' >"$D/b.conf"
printf 'c\n' >"$D/c.conf"
mkdir "$D/sub" && printf 's\n' >"$D/sub/s.conf"
for f in b.conf c.conf sub/s.conf; do
	wait_for 5 "$f rows" has_rows "$D/$f" 2
	[ "$(whats "$D/$f")" = "did not exist|created" ] || fail "$f: $(whats "$D/$f")"
done
na=$(nrows "$D/a.conf")
rm "$D/a.conf"
wait_for 5 "the a.conf deleted row" has_rows "$D/a.conf" $((na + 1))
[ "$(newest "$D/a.conf" 6)" = deleted ] || fail "rm: $(whats "$D/a.conf")"
na=$(nrows "$D/a.conf")
nx=$(nrows "$D/x.service")
mv "$D" "$D2"
for f in b.conf c.conf sub/s.conf; do
	wait_for 15 "$D2/$f rows" has_rows "$D2/$f" 2
	[ "$(whats "$D2/$f")" = "did not exist|created" ] || fail "$D2/$f: $(whats "$D2/$f")"
	wait_for 15 "$D/$f deleted" has_rows "$D/$f" 3
	[ "$(newest "$D/$f" 6)" = deleted ] && [ "$(nrows "$D/$f")" = 3 ] || fail "$D/$f: $(whats "$D/$f")"
done
[ "$(nrows "$D/a.conf")" = "$na" ] && [ "$(nrows "$D/x.service")" = "$nx" ] || fail "a row for an already deleted path"

# --- 15. While stopped ------------------------------------------------------
step "15. changes while stopped"
systemctl stop "$UNIT"
echo b2 >"$D2/b.conf"
rm "$D2/c.conf"
systemctl start "$UNIT"
INV=$(inv)
wait_for 300 "baseline: after the restart" baseline_done
[ "$(newest "$D2/b.conf" 7) $(newest "$D2/b.conf" 8) $(newest "$D2/b.conf" 9) $(newest "$D2/b.conf" 10)" = "changed while not watching" ] ||
	fail "b.conf: $(whats "$D2/b.conf")"
[ "$(rows "$D2/c.conf" | awk 'NR==1{print $7, $8, $9, $10}')" = "deleted while not watching" ] || fail "c.conf: $(whats "$D2/c.conf")"

# --- 16. Overflow -----------------------------------------------------------
step "16. overflow"
if [ $HAVE_PY = 1 ]; then
	PID=$(systemctl show -p MainPID --value "$UNIT")
	kill -STOP "$PID"
	python3 - "$D2/b.conf" "$D2/sub/s.conf" <<'EOF'
import os, sys
a, b = sys.argv[1], sys.argv[2]
for i in range(20000):
    os.chmod(a if i % 2 == 0 else b, 0o600 if i % 4 < 2 else 0o640)
EOF
	printf 'late\n' >"$D2/late.conf"
	kill -CONT "$PID"
	wait_for 15 "the overflow line" bash -c "journalctl -q -o cat _SYSTEMD_INVOCATION_ID=$INV | grep -q 'overflowed'"
	wait_for 15 "late.conf" has_rows "$D2/late.conf" 1
	whats "$D2/late.conf" | grep -q '(found by rescan)' || fail "late.conf: $(whats "$D2/late.conf")"
else
	note "skipped (no python3)"
fi

# --- 17. Loudness and no content ---------------------------------------------
step "17. tiers as journald priority; no content in the journal"
printf 'SC-SECRET-%s\n' $$ >"$INERT"
wait_for 10 "the inert file in the warning journal" bash -c "journalctl -q -o cat -p warning _SYSTEMD_INVOCATION_ID=$INV | grep -q '$INERT'"
if jr -p notice | grep -q "$D2"; then fail "a tier-4 line above info"; fi
if journalctl -q -u "$UNIT" --since "@$T0" | grep -q "SC-SECRET-$$"; then fail "file content in the journal"; fi

# --- 18. Nothing excluded, nothing written, nothing ordered after scd --------
step "18. nothing excluded recorded, nothing written, nothing after scd"
if "$SC" log -n 0 | awk 'NR>1{print $5}' | grep -Eq '\.sc-tmp-|\.swp$|~$|/sed[^/]{6}$'; then
	fail "an excluded name was recorded"
fi
[ "$(sum /etc/fstab)" = "$SUM_FSTAB" ] && [ "$(sum /etc/hosts)" = "$SUM_HOSTS" ] && [ "$(sum /etc/machine-id)" = "$SUM_MID" ] ||
	fail "a watched file changed"
if grep -rEs '^(After|Requires|Requisite|BindsTo|PartOf)=.*\bscd(-accept)?\.service' /etc/systemd /run/systemd/system /usr/lib/systemd | grep -v "^$UNITFILE:"; then
	fail "a unit is ordered after or bound to scd"
fi

step "19. PASS"
echo "PASS"
